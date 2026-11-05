"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { api } from "@/lib/api";
import { formatNumber, formatDuration, statusBadgeClass, timeAgo } from "@/components/auth";

interface Health {
  total_runs: number;
  successful_runs: number;
  failed_runs: number;
  success_rate: number;
  avg_duration_ms?: number;
}

export default function OverviewPage() {
  const catalog = useQuery({
    queryKey: ["overview-catalog"],
    queryFn: () => api.get<{ datasets: { id: string; full_name: string; asset_type: string; row_count?: number; column_count?: number; source_name?: string }[]; total: number }>("/v1/catalog/datasets?limit=100"),
  });
  const health = useQuery({
    queryKey: ["overview-health"],
    queryFn: () => api.get<{ health: Health }>("/v1/observability/health?days=7"),
  });
  const jobs = useQuery({
    queryKey: ["overview-jobs"],
    queryFn: () => api.get<{ jobs: { id: string; name: string; total_runs: number; last_status?: string; last_run_at?: string }[] }>("/v1/jobs"),
  });
  const alerts = useQuery({
    queryKey: ["overview-alerts"],
    queryFn: () => api.get<{ alerts: { id: string; title: string; severity: string; status: string; created_at: string }[] }>("/v1/alerts?status=OPEN"),
  });
  const freshness = useQuery({
    queryKey: ["overview-freshness"],
    queryFn: () => api.get<{ freshness: { dataset_id: string; full_name: string; status: string }[] }>("/v1/freshness"),
  });

  const totalDatasets = catalog.data?.total ?? 0;
  const totalColumns = (catalog.data?.datasets ?? []).reduce((sum, d) => sum + (d.column_count ?? 0), 0);
  const h = health.data?.health;
  const openAlerts = alerts.data?.alerts ?? [];
  const stale = (freshness.data?.freshness ?? []).filter((f) => f.status === "STALE" || f.status === "WARNING");

  return (
    <div style={{ display: "grid", gap: 20 }}>
      <div>
        <h1 style={{ fontSize: 22, fontWeight: 700 }}>Overview</h1>
        <p className="text-dim" style={{ fontSize: 13, marginTop: 4 }}>
          Your organization&apos;s data ecosystem at a glance.
        </p>
      </div>

      <div style={{ display: "grid", gridTemplateColumns: "repeat(4, 1fr)", gap: 14 }}>
        <Link href="/catalog" className="surface stat-card">
          <div className="label">Datasets</div>
          <div className="value">{formatNumber(totalDatasets)}</div>
        </Link>
        <Link href="/catalog" className="surface stat-card">
          <div className="label">Columns tracked</div>
          <div className="value">{formatNumber(totalColumns)}</div>
        </Link>
        <Link href="/jobs" className="surface stat-card">
          <div className="label">Pipeline runs (7d)</div>
          <div className="value">{formatNumber(h?.total_runs ?? 0)}</div>
          <div className="text-dim" style={{ fontSize: 12, marginTop: 4 }}>
            {h && h.total_runs > 0 ? `${(h.success_rate * 100).toFixed(0)}% succeeded` : "no runs yet"}
          </div>
        </Link>
        <Link href="/alerts" className="surface stat-card">
          <div className="label">Open alerts</div>
          <div className="value" style={{ color: openAlerts.length > 0 ? "var(--amber)" : undefined }}>
            {formatNumber(openAlerts.length)}
          </div>
        </Link>
      </div>

      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 14 }}>
        <div className="surface" style={{ padding: 16 }}>
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 10 }}>
            <h2 style={{ fontSize: 15, fontWeight: 700 }}>Open alerts</h2>
            <Link href="/alerts" className="text-dim" style={{ fontSize: 12 }}>View all</Link>
          </div>
          {openAlerts.length === 0 ? (
            <p className="text-dim" style={{ fontSize: 13 }}>No open alerts. Pipelines look healthy.</p>
          ) : (
            <div style={{ display: "grid", gap: 8 }}>
              {openAlerts.slice(0, 5).map((a) => (
                <div key={a.id} className="surface-2" style={{ padding: 10, display: "flex", justifyContent: "space-between", gap: 8 }}>
                  <div style={{ minWidth: 0 }}>
                    <div style={{ fontSize: 13, fontWeight: 600, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{a.title}</div>
                    <div className="text-dim" style={{ fontSize: 12 }}>{timeAgo(a.created_at)}</div>
                  </div>
                  <span className={statusBadgeClass(a.severity)}>{a.severity}</span>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="surface" style={{ padding: 16 }}>
          <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 10 }}>
            <h2 style={{ fontSize: 15, fontWeight: 700 }}>Freshness watch</h2>
            <Link href="/freshness" className="text-dim" style={{ fontSize: 12 }}>Monitor</Link>
          </div>
          {stale.length === 0 ? (
            <p className="text-dim" style={{ fontSize: 13 }}>No stale or degrading datasets.</p>
          ) : (
            <div style={{ display: "grid", gap: 8 }}>
              {stale.slice(0, 5).map((f) => (
                <div key={f.dataset_id} className="surface-2" style={{ padding: 10, display: "flex", justifyContent: "space-between" }}>
                  <span style={{ fontSize: 13 }}>{f.full_name}</span>
                  <span className={statusBadgeClass(f.status)}>{f.status}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      <div className="surface" style={{ padding: 16 }}>
        <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 10 }}>
          <h2 style={{ fontSize: 15, fontWeight: 700 }}>Pipelines</h2>
          <Link href="/jobs" className="text-dim" style={{ fontSize: 12 }}>All runs</Link>
        </div>
        {(jobs.data?.jobs ?? []).length === 0 ? (
          <p className="text-dim" style={{ fontSize: 13 }}>
            No pipeline activity observed yet. Emit OpenLineage events or ingest dbt artifacts to build the graph.
          </p>
        ) : (
          <table className="data">
            <thead>
              <tr><th>Job</th><th>Runs</th><th>Last status</th><th>Last run</th></tr>
            </thead>
            <tbody>
              {(jobs.data?.jobs ?? []).slice(0, 8).map((j) => (
                <tr key={j.id}>
                  <td style={{ fontWeight: 600 }}>{j.name}</td>
                  <td>{formatNumber(j.total_runs)}</td>
                  <td>{j.last_status ? <span className={statusBadgeClass(j.last_status)}>{j.last_status}</span> : "—"}</td>
                  <td className="text-dim">{timeAgo(j.last_run_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
