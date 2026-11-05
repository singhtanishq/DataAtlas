"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, type DatasetSummary } from "@/lib/api";
import { statusBadgeClass, useRequireIdentity } from "@/components/auth";

const CHECK_TYPES = [
  "NOT_NULL", "UNIQUE", "ROW_COUNT", "ACCEPTED_VALUES", "NUMERIC_RANGE",
  "REGEX", "FRESHNESS", "REFERENTIAL_INTEGRITY", "CUSTOM_SQL",
];

export default function QualityPage() {
  useRequireIdentity();
  const qc = useQueryClient();
  const [showBuilder, setShowBuilder] = useState(false);
  const [datasetQuery, setDatasetQuery] = useState("");
  const [selectedDataset, setSelectedDataset] = useState<DatasetSummary | null>(null);
  const [checkName, setCheckName] = useState("");
  const [checkType, setCheckType] = useState("NOT_NULL");
  const [column, setColumn] = useState("");
  const [severity, setSeverity] = useState("ERROR");
  const [customSql, setCustomSql] = useState("");
  const [error, setError] = useState<string | null>(null);

  const checks = useQuery({
    queryKey: ["quality-checks", selectedDataset?.id],
    queryFn: () =>
      api.get<{ checks: { id: string; name: string; check_type: string; severity: string; enabled: boolean }[] }>(
        `/v1/quality/checks?dataset_id=${selectedDataset!.id}`
      ),
    enabled: !!selectedDataset,
  });
  const search = useQuery({
    queryKey: ["quality-dataset-search", datasetQuery],
    queryFn: () => api.get<{ datasets: DatasetSummary[] }>(`/v1/catalog/datasets?q=${encodeURIComponent(datasetQuery)}&limit=6`),
    enabled: datasetQuery.length > 1 && showBuilder,
  });
  const runs = useQuery({
    queryKey: ["quality-runs"],
    queryFn: () => api.get<{ alerts: { id: string; title: string; severity: string; status: string; created_at: string }[] }>(`/v1/alerts`),
  });

  async function createCheck(e: React.FormEvent) {
    e.preventDefault();
    if (!selectedDataset) return;
    setError(null);
    const parameters: Record<string, unknown> = {};
    if (checkType === "CUSTOM_SQL") parameters.sql = customSql;
    try {
      await api.post("/v1/quality/checks", {
        dataset_id: selectedDataset.id,
        name: checkName,
        check_type: checkType,
        target_column: column || undefined,
        parameters,
        severity,
      });
      setCheckName(""); setColumn(""); setCustomSql("");
      setShowBuilder(false);
      await qc.invalidateQueries({ queryKey: ["quality-checks"] });
    } catch (err) {
      setError(err instanceof Error ? err.message : "creation failed");
    }
  }

  async function runAll() {
    await api.post("/v1/quality/runs").catch(() => undefined);
    setTimeout(() => {
      qc.invalidateQueries({ queryKey: ["quality-runs"] });
      qc.invalidateQueries({ queryKey: ["alerts"] });
    }, 4000);
  }

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <div>
          <h1 style={{ fontSize: 22, fontWeight: 700 }}>Quality Checks</h1>
          <p className="text-dim" style={{ fontSize: 13, marginTop: 4 }}>
            Configurable validation executed with read-only, bounded, parameterized SQL.
          </p>
        </div>
        <div style={{ display: "flex", gap: 8 }}>
          <button className="btn" onClick={runAll}>Run all checks</button>
          <button className="btn btn-primary" onClick={() => setShowBuilder((v) => !v)}>
            {showBuilder ? "Cancel" : "New check"}
          </button>
        </div>
      </div>

      {showBuilder && (
        <form className="surface" style={{ padding: 18, display: "grid", gap: 12, maxWidth: 640 }} onSubmit={createCheck}>
          {!selectedDataset ? (
            <>
              <input
                className="input"
                placeholder="Find a dataset…"
                value={datasetQuery}
                onChange={(e) => setDatasetQuery(e.target.value)}
              />
              {(search.data?.datasets ?? []).map((d) => (
                <button
                  type="button"
                  key={d.id}
                  className="surface-2"
                  style={{ padding: "8px 12px", textAlign: "left", cursor: "pointer", fontSize: 13, background: "var(--surface-2)", color: "var(--text)", border: "1px solid var(--border)", borderRadius: 7 }}
                  onClick={() => setSelectedDataset(d)}
                >
                  {d.full_name}
                </button>
              ))}
            </>
          ) : (
            <>
              <div className="surface-2" style={{ padding: "8px 12px", fontSize: 13, display: "flex", justifyContent: "space-between" }}>
                <span style={{ fontFamily: "ui-monospace, monospace" }}>{selectedDataset.full_name}</span>
                <button type="button" className="text-dim" style={{ background: "none", border: "none", cursor: "pointer" }} onClick={() => setSelectedDataset(null)}>
                  change
                </button>
              </div>
              <div style={{ display: "grid", gridTemplateColumns: "2fr 1fr", gap: 12 }}>
                <label className="text-dim" style={{ display: "grid", gap: 4, fontSize: 12 }}>
                  Check name
                  <input className="input" value={checkName} onChange={(e) => setCheckName(e.target.value)} required />
                </label>
                <label className="text-dim" style={{ display: "grid", gap: 4, fontSize: 12 }}>
                  Severity
                  <select className="input" value={severity} onChange={(e) => setSeverity(e.target.value)}>
                    <option>INFO</option><option>WARNING</option><option>ERROR</option><option>CRITICAL</option>
                  </select>
                </label>
              </div>
              <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
                <label className="text-dim" style={{ display: "grid", gap: 4, fontSize: 12 }}>
                  Check type
                  <select className="input" value={checkType} onChange={(e) => setCheckType(e.target.value)}>
                    {CHECK_TYPES.map((t) => <option key={t}>{t}</option>)}
                  </select>
                </label>
                <label className="text-dim" style={{ display: "grid", gap: 4, fontSize: 12 }}>
                  Target column (when applicable)
                  <input className="input" value={column} onChange={(e) => setColumn(e.target.value)} />
                </label>
              </div>
              {checkType === "CUSTOM_SQL" && (
                <label className="text-dim" style={{ display: "grid", gap: 4, fontSize: 12 }}>
                  SQL (single read-only SELECT; executed bounded)
                  <textarea className="input" rows={3} value={customSql} onChange={(e) => setCustomSql(e.target.value)} required style={{ fontFamily: "ui-monospace, monospace" }} />
                </label>
              )}
              {error && <div className="badge badge-red" style={{ padding: 8 }}>{error}</div>}
              <div>
                <button className="btn btn-primary" type="submit">Create check</button>
              </div>
            </>
          )}
        </form>
      )}

      {selectedDataset && !showBuilder && (
        <div className="surface" style={{ padding: 6 }}>
          {(checks.data?.checks ?? []).length === 0 ? (
            <p className="text-dim" style={{ padding: 16, fontSize: 13 }}>No checks for {selectedDataset.full_name}.</p>
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

      <div className="surface" style={{ padding: 16 }}>
        <h2 style={{ fontSize: 15, fontWeight: 700, marginBottom: 10 }}>Recent quality alerts</h2>
        {(runs.data?.alerts ?? []).length === 0 ? (
          <p className="text-dim" style={{ fontSize: 13 }}>No quality failures recorded. Run checks to evaluate data quality.</p>
        ) : (
          <div style={{ display: "grid", gap: 8 }}>
            {(runs.data?.alerts ?? []).filter((a) => a.title.startsWith("Quality")).slice(0, 6).map((a) => (
              <div key={a.id} className="surface-2" style={{ padding: 10, display: "flex", justifyContent: "space-between" }}>
                <span style={{ fontSize: 13 }}>{a.title}</span>
                <span className={statusBadgeClass(a.status)}>{a.status}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
