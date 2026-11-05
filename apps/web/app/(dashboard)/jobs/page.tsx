"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { formatDuration, formatNumber, statusBadgeClass, timeAgo, useRequireIdentity } from "@/components/auth";

export default function JobsPage() {
  useRequireIdentity();
  const jobs = useQuery({
    queryKey: ["jobs"],
    queryFn: () => api.get<{ jobs: { id: string; name: string; namespace: string; total_runs: number; successful_runs: number; failed_runs: number; last_status?: string; last_run_at?: string; avg_duration_ms?: number }[] }>("/v1/jobs"),
  });
  const health = useQuery({
    queryKey: ["jobs-health"],
    queryFn: () => api.get<{ health: { total_runs: number; successful_runs: number; failed_runs: number; success_rate: number; avg_duration_ms?: number; window_days: number } }>(
      "/v1/observability/health?days=7"
    ),
  });

  const h = health.data?.health;

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <div>
        <h1 style={{ fontSize: 22, fontWeight: 700 }}>Pipeline Runs</h1>
        <p className="text-dim" style={{ fontSize: 13, marginTop: 4 }}>
          Jobs observed from OpenLineage events, with run outcomes and durations.
        </p>
      </div>

      <div style={{ display: "grid", gridTemplateColumns: "repeat(4, 1fr)", gap: 14 }}>
        <div className="surface stat-card"><div className="label">Runs (7d)</div><div className="value">{formatNumber(h?.total_runs ?? 0)}</div></div>
        <div className="surface stat-card"><div className="label">Succeeded</div><div className="value" style={{ color: "var(--green)" }}>{formatNumber(h?.successful_runs ?? 0)}</div></div>
        <div className="surface stat-card"><div className="label">Failed</div><div className="value" style={{ color: "var(--red)" }}>{formatNumber(h?.failed_runs ?? 0)}</div></div>
        <div className="surface stat-card"><div className="label">Avg duration</div><div className="value" style={{ fontSize: 20 }}>{formatDuration(h?.avg_duration_ms)}</div></div>
      </div>

      <div className="surface" style={{ padding: 6 }}>
        {(jobs.data?.jobs ?? []).length === 0 ? (
          <p className="text-dim" style={{ padding: 16, fontSize: 13 }}>
            No jobs observed yet. Emit OpenLineage events from the SDK to register pipelines.
          </p>
        ) : (
          <table className="data">
            <thead><tr><th>Job</th><th>Namespace</th><th>Runs</th><th>Success</th><th>Failed</th><th>Avg duration</th><th>Last run</th></tr></thead>
            <tbody>
              {(jobs.data?.jobs ?? []).map((j) => (
                <tr key={j.id}>
                  <td style={{ fontWeight: 600 }}>{j.name}</td>
                  <td className="text-dim">{j.namespace}</td>
                  <td>{formatNumber(j.total_runs)}</td>
                  <td><span className={statusBadgeClass("SUCCEEDED")}>{formatNumber(j.successful_runs)}</span></td>
                  <td><span className={j.failed_runs > 0 ? "badge badge-red" : "badge badge-gray"}>{formatNumber(j.failed_runs)}</span></td>
                  <td>{formatDuration(j.avg_duration_ms)}</td>
                  <td>{j.last_status ? <span className={statusBadgeClass(j.last_status)}>{j.last_status}</span> : "—"} <span className="text-dim" style={{ fontSize: 12 }}>{timeAgo(j.last_run_at)}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
