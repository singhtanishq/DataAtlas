"use client";

import { useQuery } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { timeAgo, useRequireIdentity } from "@/components/auth";

export default function AuditPage() {
  useRequireIdentity();
  const audit = useQuery({
    queryKey: ["audit"],
    queryFn: () => api.get<{ entries: { id: number; actor_kind: string; action: string; entity_type: string; entity_id?: string; created_at: string }[] }>("/v1/audit-logs"),
  });

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <div>
        <h1 style={{ fontSize: 22, fontWeight: 700 }}>Audit Logs</h1>
        <p className="text-dim" style={{ fontSize: 13, marginTop: 4 }}>
          Recorded governance and operational actions in your organization.
        </p>
      </div>
      <div className="surface" style={{ padding: 6 }}>
        {(audit.data?.entries ?? []).length === 0 ? (
          <p className="text-dim" style={{ padding: 16, fontSize: 13 }}>No audit entries yet.</p>
        ) : (
          <table className="data">
            <thead><tr><th>Action</th><th>Entity</th><th>Actor</th><th>When</th></tr></thead>
            <tbody>
              {(audit.data?.entries ?? []).map((e) => (
                <tr key={e.id}>
                  <td style={{ fontWeight: 600 }}>{e.action}</td>
                  <td className="text-dim">{e.entity_type}</td>
                  <td><span className="badge badge-gray">{e.actor_kind}</span></td>
                  <td className="text-dim">{timeAgo(e.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
