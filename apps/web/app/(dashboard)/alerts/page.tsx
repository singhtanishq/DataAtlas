"use client";

import { useState } from "react";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Alert } from "@/lib/api";
import { statusBadgeClass, timeAgo, useRequireIdentity } from "@/components/auth";

export default function AlertsPage() {
  useRequireIdentity();
  const qc = useQueryClient();
  const [filter, setFilter] = useState("OPEN");
  const alerts = useQuery({
    queryKey: ["alerts", filter],
    queryFn: () => api.get<{ alerts: Alert[] }>(`/v1/alerts?status=${filter}`),
  });

  async function resolve(id: string) {
    await api.post(`/v1/alerts/${id}/resolve`, { note: "resolved from UI" });
    qc.invalidateQueries({ queryKey: ["alerts"] });
  }

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <div>
          <h1 style={{ fontSize: 22, fontWeight: 700 }}>Alerts</h1>
          <p className="text-dim" style={{ fontSize: 13, marginTop: 4 }}>
            Quality failures, freshness violations and ingestion problems.
          </p>
        </div>
        <div style={{ display: "flex", gap: 6 }}>
          {["OPEN", "ACKNOWLEDGED", "RESOLVED", ""].map((s) => (
            <button
              key={s || "all"}
              className="btn"
              style={filter === s ? { borderColor: "var(--accent)", color: "var(--accent)" } : undefined}
              onClick={() => setFilter(s)}
            >
              {s || "All"}
            </button>
          ))}
        </div>
      </div>

      <div style={{ display: "grid", gap: 10 }}>
        {(alerts.data?.alerts ?? []).length === 0 ? (
          <div className="surface" style={{ padding: 24 }}>
            <p className="text-dim" style={{ fontSize: 13 }}>No {filter ? filter.toLowerCase() + " " : ""}alerts.</p>
          </div>
        ) : (
          (alerts.data?.alerts ?? []).map((a) => (
            <div key={a.id} className="surface" style={{ padding: 14, display: "flex", justifyContent: "space-between", gap: 12, alignItems: "center" }}>
              <div style={{ minWidth: 0 }}>
                <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 4 }}>
                  <span className={statusBadgeClass(a.severity)}>{a.severity}</span>
                  <span style={{ fontSize: 14, fontWeight: 600 }}>{a.title}</span>
                </div>
                {a.summary && <div className="text-dim" style={{ fontSize: 12 }}>{a.summary}</div>}
                <div className="text-dim" style={{ fontSize: 11, marginTop: 4 }}>{a.alert_type} · {timeAgo(a.created_at)}</div>
              </div>
              {a.status === "OPEN" && (
                <button className="btn" style={{ flexShrink: 0 }} onClick={() => resolve(a.id)}>Resolve</button>
              )}
            </div>
          ))
        )}
      </div>
    </div>
  );
}
