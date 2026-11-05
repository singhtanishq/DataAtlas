"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { formatAge, statusBadgeClass, useRequireIdentity } from "@/components/auth";

export default function FreshnessPage() {
  useRequireIdentity();
  const freshness = useQuery({
    queryKey: ["freshness"],
    queryFn: () => api.get<{ freshness: { dataset_id: string; full_name: string; asset_type: string; status: string; age_seconds?: number; expected_interval_seconds?: number }[] }>("/v1/freshness"),
  });

  const rows = freshness.data?.freshness ?? [];
  const counts = rows.reduce<Record<string, number>>((acc, f) => {
    acc[f.status] = (acc[f.status] ?? 0) + 1;
    return acc;
  }, {});

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <div>
        <h1 style={{ fontSize: 22, fontWeight: 700 }}>Freshness Monitor</h1>
        <p className="text-dim" style={{ fontSize: 13, marginTop: 4 }}>
          Expected vs. observed freshness. Observations come from successful pipeline runs — never from metadata scan times.
        </p>
      </div>

      <div style={{ display: "flex", gap: 8 }}>
        {["FRESH", "WARNING", "STALE", "UNKNOWN"].map((s) => (
          <span key={s} className={statusBadgeClass(s === "FRESH" ? "OK" : s === "UNKNOWN" ? "" : s)}>
            {s}: {counts[s] ?? 0}
          </span>
        ))}
      </div>

      <div className="surface" style={{ padding: 6 }}>
        {rows.length === 0 ? (
          <p className="text-dim" style={{ padding: 16, fontSize: 13 }}>No datasets observed yet.</p>
        ) : (
          <table className="data">
            <thead><tr><th>Dataset</th><th>Type</th><th>Status</th><th>Last evidence</th></tr></thead>
            <tbody>
              {rows.map((f) => (
                <tr key={f.dataset_id}>
                  <td style={{ fontFamily: "ui-monospace, monospace", fontSize: 12 }}>{f.full_name}</td>
                  <td className="text-dim">{f.asset_type}</td>
                  <td><span className={statusBadgeClass(f.status === "FRESH" ? "OK" : f.status)}>{f.status}</span></td>
                  <td className="text-dim">{formatAge(f.age_seconds)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
