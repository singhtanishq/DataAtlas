package lineage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/singhtanishq/DataAtlas/services/control-plane/internal/db"
)

// GraphNode is one asset in a lineage graph view.
type GraphNode struct {
	AssetID   string  `json:"asset_id"`
	Namespace string  `json:"namespace"`
	Name      string  `json:"name"`
	FullName  string  `json:"full_name"`
	AssetType string  `json:"asset_type"`
	Depth     int     `json:"depth"`
	ViaJob    *string `json:"via_job,omitempty"`
}

// GraphEdge is one edge in a lineage graph view.
type GraphEdge struct {
	SourceAssetID string     `json:"source_asset_id"`
	TargetAssetID string     `json:"target_asset_id"`
	JobID         *string    `json:"job_id"`
	JobName       *string    `json:"job_name"`
	Origin        string     `json:"origin"`
	Confidence    string     `json:"confidence"`
	LastSeenAt    *time.Time `json:"last_seen_at"`
}

// GraphView bundles nodes and edges for visualization.
type GraphView struct {
	RootAssetID string      `json:"root_asset_id"`
	Direction   string      `json:"direction"`
	Depth       int         `json:"depth"`
	Nodes       []GraphNode `json:"nodes"`
	Edges       []GraphEdge `json:"edges"`
}

const maxTraversalDepth = 10

// ErrNotFound is returned when the root asset does not exist in the org.
var ErrNotFound = errors.New("lineage: asset not found")

// Traverse walks upstream or downstream from an asset via a recursive CTE.
func (s *Service) Traverse(
	ctx context.Context, organizationID, assetID, direction string, depth int,
) (GraphView, error) {
	if direction != "upstream" && direction != "downstream" {
		return GraphView{}, fmt.Errorf("direction must be upstream or downstream")
	}
	if depth <= 0 || depth > maxTraversalDepth {
		depth = 5
	}
	// Asset must exist in the tenant.
	var exists bool
	err := s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM data_assets WHERE id = $1 AND deleted_at IS NULL)`,
			assetID).Scan(&exists)
	})
	if err != nil {
		return GraphView{}, err
	}
	if !exists {
		return GraphView{}, ErrNotFound
	}

	joinCol := "target_asset_id"
	nextCol := "source_asset_id"
	if direction == "downstream" {
		joinCol = "source_asset_id"
		nextCol = "target_asset_id"
	}

	query := fmt.Sprintf(`
		WITH RECURSIVE walk(asset_id, job_id, depth) AS (
			SELECT e.%s, e.job_id, 1
			FROM lineage_edges e
			WHERE e.%s = $1 AND e.organization_id = $2
			UNION
			SELECT e.%s, e.job_id, w.depth + 1
			FROM lineage_edges e
			JOIN walk w ON e.%s = w.asset_id
			WHERE w.depth < $3 AND e.organization_id = $2
		)
		SELECT DISTINCT asset_id::text, job_id::text, depth FROM walk`,
		nextCol, joinCol, nextCol, joinCol)

	var nodeIDs []struct {
		AssetID string
		JobID   *string
		Depth   int
	}
	err = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, assetID, uuid.MustParse(organizationID), depth)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r struct {
				AssetID string
				JobID   *string
				Depth   int
			}
			if err := rows.Scan(&r.AssetID, &r.JobID, &r.Depth); err != nil {
				return err
			}
			nodeIDs = append(nodeIDs, r)
		}
		return rows.Err()
	})
	if err != nil {
		return GraphView{}, err
	}

	// Hydrate nodes and collect edges among them.
	view := GraphView{RootAssetID: assetID, Direction: direction, Depth: depth}
	seen := map[string]bool{assetID: true}
	var root struct {
		Namespace, Name, FullName, AssetType string
	}
	err = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT namespace, name, full_name, asset_type FROM data_assets WHERE id = $1`,
			assetID).Scan(&root.Namespace, &root.Name, &root.FullName, &root.AssetType); err != nil {
			return err
		}
		view.Nodes = append(view.Nodes, GraphNode{
			AssetID: assetID, Namespace: root.Namespace, Name: root.Name,
			FullName: root.FullName, AssetType: root.AssetType, Depth: 0,
		})
		for _, n := range nodeIDs {
			if seen[n.AssetID] {
				continue
			}
			seen[n.AssetID] = true
			var node GraphNode
			if err := tx.QueryRow(ctx, `
				SELECT namespace, name, full_name, asset_type FROM data_assets
				WHERE id = $1 AND deleted_at IS NULL`, n.AssetID).Scan(
				&node.Namespace, &node.Name, &node.FullName, &node.AssetType); err != nil {
				if errors.Is(err, errNoRowsSentinel) {
					continue
				}
				return err
			}
			node.AssetID = n.AssetID
			node.Depth = n.Depth
			node.ViaJob = n.JobID
			view.Nodes = append(view.Nodes, node)
		}
		// Edges between the collected nodes.
		edgeRows, err := tx.Query(ctx, fmt.Sprintf(`
			SELECT e.source_asset_id::text, e.target_asset_id::text,
			       e.job_id::text, j.name, e.origin, e.confidence, e.last_seen_at
			FROM lineage_edges e
			LEFT JOIN jobs j ON j.id = e.job_id
			WHERE e.organization_id = $1 AND e.%s = ANY($2::uuid[])
			ORDER BY e.last_seen_at DESC NULLS LAST`, joinCol),
			uuid.MustParse(organizationID), idArrayText(view.Nodes))
		if err != nil {
			return err
		}
		defer edgeRows.Close()
		edgeSeen := map[string]bool{}
		for edgeRows.Next() {
			var e GraphEdge
			if err := edgeRows.Scan(&e.SourceAssetID, &e.TargetAssetID, &e.JobID,
				&e.JobName, &e.Origin, &e.Confidence, &e.LastSeenAt); err != nil {
				return err
			}
			key := e.SourceAssetID + "->" + e.TargetAssetID
			if edgeSeen[key] {
				continue
			}
			edgeSeen[key] = true
			if seen[e.SourceAssetID] && seen[e.TargetAssetID] {
				view.Edges = append(view.Edges, e)
			}
		}
		return edgeRows.Err()
	})
	return view, err
}

var errNoRowsSentinel = errors.New("no rows")

// ImpactAnalysis summarizes what depends on an asset.
type ImpactAnalysis struct {
	AssetID             string      `json:"asset_id"`
	DownstreamDatasets  []GraphNode `json:"downstream_datasets"`
	DependentJobs       []string    `json:"dependent_jobs"`
	BreakingEdgeCount   int         `json:"explicit_edge_count"`
	InferredEdgeCount   int         `json:"inferred_edge_count"`
	QualityChecksAtRisk []string    `json:"quality_checks_at_risk"`
	Summary             string      `json:"summary"`
}

// AnalyzeImpact walks downstream and aggregates affected resources.
func (s *Service) AnalyzeImpact(ctx context.Context, organizationID, assetID string) (ImpactAnalysis, error) {
	downstream, err := s.Traverse(ctx, organizationID, assetID, "downstream", maxTraversalDepth)
	if err != nil {
		return ImpactAnalysis{}, err
	}

	analysis := ImpactAnalysis{AssetID: assetID}
	for _, n := range downstream.Nodes {
		if n.AssetID == assetID {
			continue
		}
		analysis.DownstreamDatasets = append(analysis.DownstreamDatasets, n)
	}

	// Affected asset ids as text[] for parameterized ANY() matches.
	ids := idArrayText(downstream.Nodes)
	orgUUID := uuid.MustParse(organizationID)

	// Jobs that consume the asset or any affected downstream dataset.
	err = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT j.id::text, j.name FROM jobs j
			JOIN job_inputs ji ON ji.job_id = j.id
			JOIN datasets d ON d.id = ji.dataset_id
			WHERE d.asset_id = ANY($1::uuid[])`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return err
			}
			analysis.DependentJobs = append(analysis.DependentJobs, name)
		}
		// Edge confidence census across the affected subgraph.
		return tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE confidence = 'EXPLICIT'),
			       count(*) FILTER (WHERE confidence = 'INFERRED')
			FROM lineage_edges
			WHERE organization_id = $1
			  AND (source_asset_id = ANY($2::uuid[]) OR target_asset_id = ANY($2::uuid[]))`,
			orgUUID, ids).Scan(
			&analysis.BreakingEdgeCount, &analysis.InferredEdgeCount)
	})
	if err != nil {
		return ImpactAnalysis{}, err
	}

	// Quality checks attached to any affected dataset.
	err = s.pool.WithOrg(ctx, organizationID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT qc.name FROM quality_checks qc
			JOIN datasets d ON d.id = qc.dataset_id
			WHERE d.asset_id = ANY($1::uuid[]) AND qc.deleted_at IS NULL`, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			analysis.QualityChecksAtRisk = append(analysis.QualityChecksAtRisk, name)
		}
		return nil
	})
	if err != nil {
		return ImpactAnalysis{}, err
	}

	analysis.Summary = fmt.Sprintf(
		"%d downstream datasets, %d dependent jobs, %d quality checks affected",
		len(analysis.DownstreamDatasets), len(analysis.DependentJobs),
		len(analysis.QualityChecksAtRisk))
	return analysis, nil
}

func idArrayText(nodes []GraphNode) []string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if _, err := uuid.Parse(n.AssetID); err == nil {
			ids = append(ids, n.AssetID)
		}
	}
	return ids
}

var _ = json.Marshal
var _ = db.OrgScopeKey
