"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useParams } from "next/navigation";
import { useState } from "react";
import { api, type Column, type DatasetDetails } from "@/lib/api";
import { formatNumber, statusBadgeClass, useRequireIdentity } from "@/components/auth";

type Tab = "overview" | "columns" | "lineage" | "profile" | "quality" | "governance";

export default function DatasetDetailPage() {
  useRequireIdentity();
  const params = useParams<{ id: string }>();
  const datasetId = params.id;
  const qc = useQueryClient();
  const [tab, setTab] = useState<Tab>("overview");
  const [description, setDescription] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const details = useQuery({
    queryKey: ["dataset", datasetId],
    queryFn: () => api.get<{ dataset: DatasetDetails }>(`/v1/catalog/datasets/${datasetId}`),
  });
  const columns = useQuery({
    queryKey: ["dataset-columns", datasetId],
    queryFn: () => api.get<{ columns: Column[] }>(`/v1/catalog/datasets/${datasetId}/columns`),
  });
  const profile = useQuery({
    queryKey: ["dataset-profile", datasetId],
    queryFn: () =>
      api.get<{ profile: { row_count?: number; columns: { name: string; metrics: { metric_name: string; value?: number; value_text?: string; value_type: string }[] }[] } }>(
        `/v1/catalog/datasets/${datasetId}/profile`
      ).catch(() => null),
  });
  const checks = useQuery({
    queryKey: ["dataset-checks", datasetId],
    queryFn: () => api.get<{ checks: { id: string; name: string; check_type: string; severity: string; enabled: boolean }[] }>(
      `/v1/quality/checks?dataset_id=${datasetId}`
    ),
  });

  const d = details.data?.dataset;
  const assetId = d?.asset_id ?? "";
  const lineage = useQuery({
    queryKey: ["dataset-lineage", assetId],
    queryFn: () =>
      api.get<{ graph: { nodes: { asset_id: string; full_name: string; asset_type: string; depth: number }[]; edges: { source_asset_id: string; target_asset_id: string; job_name?: string }[] } }>(
        `/v1/lineage/${assetId}/upstream?depth=4`
      ).catch(() => null),
    enabled: tab === "lineage" && assetId !== "",
  });

  async function saveDescription() {
    if (description === null) return;
    setSaving(true);
    try {
      await api.patch(`/v1/catalog/datasets/${datasetId}`, { description });
      await qc.invalidateQueries({ queryKey: ["dataset", datasetId] });
      setDescription(null);
    } finally {
      setSaving(false);
    }
  }

  async function triggerProfile() {
    await api.post(`/v1/catalog/datasets/${datasetId}/profile`).catch(() => undefined);
    setTimeout(() => qc.invalidateQueries({ queryKey: ["dataset-profile", datasetId] }), 6000);
  }

  if (details.isLoading) {
    return <p className="text-dim" style={{ fontSize: 13 }}>Loading dataset…</p>;
  }
  if (details.isError || !d) {
    return <p className="text-dim" style={{ fontSize: 13 }}>Dataset not found.</p>;
  }

  const TABS: { key: Tab; label: string }[] = [
    { key: "overview", label: "Overview" },
    { key: "columns", label: `Columns (${d.column_count ?? "—"})` },
    { key: "profile", label: "Profile" },
    { key: "quality", label: "Quality" },
    { key: "lineage", label: "Lineage" },
    { key: "governance", label: "Governance" },
  ];

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <div>
        <div style={{ display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap" }}>
          <h1 style={{ fontSize: 20, fontWeight: 700, fontFamily: "ui-monospace, monospace" }}>
            {d.full_name}
          </h1>
          <span className={statusBadgeClass(d.asset_type)}>{d.asset_type}</span>
          {d.current_version_is_breaking && <span className="badge badge-red">breaking change</span>}
        </div>
        <p className="text-dim" style={{ fontSize: 13, marginTop: 6 }}>
          {d.description ?? "No description yet."} · Source: {d.source_name ?? "external"} · Schema v{d.current_schema_version ?? "—"}
        </p>
      </div>

      <div style={{ display: "flex", gap: 4, borderBottom: "1px solid var(--border)", paddingBottom: 0 }}>
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            style={{
              padding: "8px 14px", fontSize: 13, fontWeight: 600,
              background: "none", border: "none", cursor: "pointer",
              color: tab === t.key ? "var(--accent)" : "var(--text-dim)",
              borderBottom: tab === t.key ? "2px solid var(--accent)" : "2px solid transparent",
            }}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "overview" && (
        <div style={{ display: "grid", gridTemplateColumns: "repeat(4, 1fr)", gap: 14 }}>
          <div className="surface stat-card"><div className="label">Rows</div><div className="value">{formatNumber(d.row_count)}</div><div className="text-dim" style={{ fontSize: 11 }}>{d.row_count_method ?? ""}</div></div>
          <div className="surface stat-card"><div className="label">Columns</div><div className="value">{formatNumber(d.column_count)}</div></div>
          <div className="surface stat-card"><div className="label">Schema version</div><div className="value">{d.current_schema_version ?? "—"}</div></div>
          <div className="surface stat-card"><div className="label">Owners</div><div className="value">{(d.owners ?? []).length}</div></div>
        </div>
      )}

      {tab === "columns" && (
        <div className="surface" style={{ padding: 6 }}>
          <table className="data">
            <thead><tr><th>#</th><th>Column</th><th>Type</th><th>Nullable</th><th>Keys</th><th>Description</th></tr></thead>
            <tbody>
              {(columns.data?.columns ?? []).map((c) => (
                <tr key={c.id}>
                  <td className="text-dim">{c.ordinal}</td>
                  <td style={{ fontFamily: "ui-monospace, monospace" }}>{c.name}</td>
                  <td className="text-dim">{c.data_type}</td>
                  <td>{c.nullable ? <span className="badge badge-gray">nullable</span> : <span className="badge badge-blue">NOT NULL</span>}</td>
                  <td>
                    {c.is_primary_key && <span className="badge badge-blue" style={{ marginRight: 4 }}>PK</span>}
                    {c.is_foreign_key && <span className="badge badge-gray">FK → {c.fk_reference}</span>}
                  </td>
                  <td className="text-dim" style={{ fontSize: 12 }}>{c.description ?? ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {tab === "profile" && (
        <div style={{ display: "grid", gap: 12 }}>
          <div>
            <button className="btn btn-primary" onClick={triggerProfile}>Run profiling</button>
            <span className="text-dim" style={{ fontSize: 12, marginLeft: 10 }}>
              Samples up to 100k rows; every metric records how it was computed.
            </span>
          </div>
          {!profile.data ? (
            <p className="text-dim" style={{ fontSize: 13 }}>No profile yet — run profiling to generate statistics.</p>
          ) : (
            <div className="surface" style={{ padding: 6 }}>
              <table className="data">
                <thead><tr><th>Column</th><th>Null %</th><th>Distinct</th><th>Uniqueness</th><th>Min</th><th>Max</th><th>Provenance</th></tr></thead>
                <tbody>
                  {(profile.data.profile.columns ?? []).map((c) => {
                    const get = (name: string) => c.metrics.find((m) => m.metric_name === name);
                    const val = (name: string) => {
                      const m = get(name);
                      return m ? (m.value !== null && m.value !== undefined ? formatNumber(m.value) : m.value_text ?? "—") : "—";
                    };
                    return (
                      <tr key={c.name}>
                        <td style={{ fontFamily: "ui-monospace, monospace" }}>{c.name}</td>
                        <td>{val("null_percentage")}</td>
                        <td>{val("distinct_count")}</td>
                        <td>{val("uniqueness_percentage")}</td>
                        <td>{val("min_value")}</td>
                        <td>{val("max_value")}</td>
                        <td className="text-dim" style={{ fontSize: 11 }}>{c.metrics[0]?.value_type ?? ""}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}

      {tab === "quality" && (
        <div className="surface" style={{ padding: 6 }}>
          {(checks.data?.checks ?? []).length === 0 ? (
            <p className="text-dim" style={{ padding: 16, fontSize: 13 }}>No checks configured for this dataset.</p>
          ) : (
            <table className="data">
              <thead><tr><th>Check</th><th>Type</th><th>Severity</th><th>Enabled</th></tr></thead>
              <tbody>
                {(checks.data?.checks ?? []).map((c) => (
                  <tr key={c.id}>
                    <td style={{ fontWeight: 600 }}>{c.name}</td>
                    <td className="text-dim">{c.check_type}</td>
                    <td><span className={statusBadgeClass(c.severity === "ERROR" ? "FAILED" : "WARNING")}>{c.severity}</span></td>
                    <td>{c.enabled ? "yes" : "no"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {tab === "lineage" && (
        <div style={{ display: "grid", gap: 12 }}>
          <p className="text-dim" style={{ fontSize: 13 }}>Upstream dependencies (evidence-based edges from OpenLineage and dbt events):</p>
          {!lineage.data ? (
            <p className="text-dim" style={{ fontSize: 13 }}>No lineage observed for this dataset yet.</p>
          ) : (
            <div style={{ display: "grid", gap: 6 }}>
              {[...lineage.data.graph.nodes]
                .sort((a, b) => b.depth - a.depth)
                .map((n) => (
                  <div key={n.asset_id} className="surface-2" style={{ padding: "9px 14px", display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                    <span style={{ fontFamily: "ui-monospace, monospace", fontSize: 13 }}>{n.full_name}</span>
                    <span className="badge badge-gray">{n.depth === 0 ? "this dataset" : `depth ${n.depth}`}</span>
                  </div>
                ))}
            </div>
          )}
        </div>
      )}

      {tab === "governance" && (
        <div style={{ display: "grid", gap: 12, maxWidth: 640 }}>
          <div className="surface" style={{ padding: 16, display: "grid", gap: 10 }}>
            <h3 style={{ fontSize: 14, fontWeight: 700 }}>Description</h3>
            {description === null ? (
              <p style={{ fontSize: 13 }}>{d.description ?? "No description yet."}</p>
            ) : (
              <textarea className="input" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} />
            )}
            {description === null ? (
              <button className="btn" style={{ justifySelf: "start" }} onClick={() => setDescription(d.description ?? "")}>Edit</button>
            ) : (
              <div style={{ display: "flex", gap: 8 }}>
                <button className="btn btn-primary" disabled={saving} onClick={saveDescription}>{saving ? "Saving…" : "Save"}</button>
                <button className="btn" onClick={() => setDescription(null)}>Cancel</button>
              </div>
            )}
          </div>
          <div className="surface" style={{ padding: 16, display: "grid", gap: 8 }}>
            <h3 style={{ fontSize: 14, fontWeight: 700 }}>Owners</h3>
            {(d.owners ?? []).length === 0 ? (
              <p className="text-dim" style={{ fontSize: 13 }}>No owners assigned.</p>
            ) : (
              (d.owners ?? []).map((o) => (
                <div key={o.assignment_id} style={{ display: "flex", justifyContent: "space-between", fontSize: 13 }}>
                  <span>{o.user_name ?? o.team_name ?? "unknown"}</span>
                  <span className="badge badge-blue">{o.ownership_role}</span>
                </div>
              ))
            )}
          </div>
          <div className="surface" style={{ padding: 16, display: "grid", gap: 8 }}>
            <h3 style={{ fontSize: 14, fontWeight: 700 }}>Tags</h3>
            <div style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
              {(d.tags ?? []).length === 0 ? (
                <span className="text-dim" style={{ fontSize: 13 }}>No tags.</span>
              ) : (
                (d.tags ?? []).map((t) => <span key={t.id} className="badge badge-gray">{t.name}</span>)
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
