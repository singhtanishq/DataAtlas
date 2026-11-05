"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useState } from "react";
import { api, type GlossaryTerm } from "@/lib/api";
import { statusBadgeClass, useRequireIdentity } from "@/components/auth";

interface Node {
  asset_id: string;
  full_name: string;
  asset_type: string;
  depth: number;
}

const TYPE_COLORS: Record<string, string> = {
  TABLE: "#5b8def",
  VIEW: "#8b7def",
  DBT_MODEL: "#3fb27f",
  DBT_SOURCE: "#e0a24a",
  EXTERNAL: "#8b93a7",
  FILE: "#3fb27f",
};

export default function LineagePage() {
  useRequireIdentity();
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<{ id: string; name: string } | null>(null);

  const search = useQuery({
    queryKey: ["lineage-search", query],
    queryFn: () => api.get<{ datasets: { id: string; asset_id: string; full_name: string }[] }>(
      `/v1/catalog/datasets?q=${encodeURIComponent(query)}&limit=8`
    ),
    enabled: query.length > 1,
  });

  const graph = useQuery({
    queryKey: ["lineage-graph", selected?.id],
    queryFn: () =>
      api.get<{ graph: { nodes: Node[]; edges: { source_asset_id: string; target_asset_id: string; job_name?: string }[] } }>(
        `/v1/lineage/${selected!.id}/upstream?depth=6`
      ),
    enabled: !!selected,
  });

  const nodes = graph.data?.graph.nodes ?? [];
  const edges = graph.data?.graph.edges ?? [];

  // Layered layout: group nodes by depth (upstream depth 6 = leftmost).
  const maxDepth = nodes.reduce((m, n) => Math.max(m, n.depth), 0);
  const layers = new Map<number, Node[]>();
  for (const n of nodes) {
    const layer = maxDepth - n.depth; // depth 0 (root) → rightmost
    if (!layers.has(layer)) layers.set(layer, []);
    layers.get(layer)!.push(n);
  }
  const layerKeys = [...layers.keys()].sort((a, b) => a - b);
  const posById = new Map<string, { x: number; y: number }>();
  const COL_W = 250, ROW_H = 74;
  layerKeys.forEach((layer, li) => {
    const group = layers.get(layer)!;
    group.forEach((n, i) => {
      posById.set(n.asset_id, { x: 40 + li * COL_W, y: 40 + i * ROW_H });
    });
  });
  const height = Math.max(...[...layers.values()].map((g) => g.length), 1) * ROW_H + 60;
  const width = layerKeys.length * COL_W + 60;

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <div>
        <h1 style={{ fontSize: 22, fontWeight: 700 }}>Data Lineage</h1>
        <p className="text-dim" style={{ fontSize: 13, marginTop: 4 }}>
          Upstream dependency graphs built from OpenLineage events, dbt artifacts and observed runs.
        </p>
      </div>

      <div style={{ display: "grid", gap: 10, maxWidth: 480 }}>
        <input
          className="input"
          placeholder="Search a dataset to focus the graph…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        {search.data && query.length > 1 && (
          <div className="surface" style={{ padding: 6 }}>
            {(search.data.datasets ?? []).map((d) => (
              <button
                key={d.id}
                className="surface-2"
                style={{
                  display: "block", width: "100%", textAlign: "left", padding: "8px 12px",
                  marginBottom: 4, fontSize: 13, border: "1px solid var(--border)", borderRadius: 7,
                  cursor: "pointer", background: "var(--surface-2)", color: "var(--text)",
                }}
                onClick={() => {
                  setSelected({ id: d.asset_id, name: d.full_name });
                  setQuery("");
                }}
              >
                <span style={{ fontFamily: "ui-monospace, monospace" }}>{d.full_name}</span>
              </button>
            ))}
          </div>
        )}
      </div>

      {!selected ? (
        <div className="surface" style={{ padding: 24 }}>
          <p className="text-dim" style={{ fontSize: 13 }}>
            Select a dataset above to render its upstream lineage. Nodes are laid out by dependency depth; edges show the producing job when known.
          </p>
        </div>
      ) : graph.isLoading ? (
        <p className="text-dim" style={{ fontSize: 13 }}>Traversing lineage…</p>
      ) : (
        <>
          <div style={{ display: "flex", alignItems: "center", gap: 10 }}>
            <h2 style={{ fontSize: 15, fontWeight: 700, fontFamily: "ui-monospace, monospace" }}>{selected.name}</h2>
            <span className="badge badge-blue">upstream</span>
            <span className="text-dim" style={{ fontSize: 12 }}>{nodes.length} nodes · {edges.length} edges</span>
          </div>
          <div className="surface" style={{ padding: 12, overflowX: "auto" }}>
            <svg width={width} height={height} style={{ minWidth: "100%" }}>
              {edges.map((e, i) => {
                const a = posById.get(e.source_asset_id);
                const b = posById.get(e.target_asset_id);
                if (!a || !b) return null;
                const x1 = a.x + 200, y1 = a.y + 20;
                const x2 = b.x, y2 = b.y + 20;
                const mx = (x1 + x2) / 2;
                return (
                  <g key={i}>
                    <path
                      d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
                      stroke="var(--accent)" strokeWidth={1.4} fill="none" opacity={0.55}
                    />
                  </g>
                );
              })}
              {[...posById.entries()].map(([id, p]) => {
                const n = nodes.find((x) => x.asset_id === id)!;
                const color = TYPE_COLORS[n.asset_type] ?? "#8b93a7";
                return (
                  <g key={id}>
                    <rect
                      x={p.x} y={p.y} width={200} height={40} rx={8}
                      fill="var(--surface-2)" stroke={color} strokeWidth={1.4}
                    />
                    <text x={p.x + 12} y={p.y + 18} fill="var(--text)" fontSize={11} fontWeight={600} style={{ fontFamily: "ui-monospace, monospace" }}>
                      {n.full_name.length > 24 ? n.full_name.slice(0, 24) + "…" : n.full_name}
                    </text>
                    <text x={p.x + 12} y={p.y + 32} fill="var(--text-dim)" fontSize={10}>
                      {n.asset_type}
                    </text>
                  </g>
                );
              })}
            </svg>
          </div>
          <p className="text-dim" style={{ fontSize: 12 }}>
            Edges carry evidence: EXPLICIT edges come from authoritative events; INFERRED edges are heuristic and never displayed as verified.
          </p>
        </>
      )}
    </div>
  );
}
