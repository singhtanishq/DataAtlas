"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, type Source } from "@/lib/api";
import { statusBadgeClass, timeAgo } from "@/components/auth";

const CONNECTORS = [
  { key: "postgres", label: "PostgreSQL" },
  { key: "csv_file", label: "CSV File" },
  { key: "parquet_file", label: "Parquet File" },
];

export default function SourcesPage() {
  const qc = useQueryClient();
  const sources = useQuery({
    queryKey: ["sources"],
    queryFn: () => api.get<{ sources: Source[] }>("/v1/sources"),
  });
  const [showForm, setShowForm] = useState(false);
  const [name, setName] = useState("");
  const [connector, setConnector] = useState("postgres");
  const [host, setHost] = useState("localhost");
  const [port, setPort] = useState("5432");
  const [database, setDatabase] = useState("dataatlas");
  const [user, setUser] = useState("dataatlas_app");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<Record<string, string>>({});

  async function registerSource(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.post("/v1/sources", {
        name,
        connector_key: connector,
        config: { host, port: Number(port), database, user },
        credentials: { password },
      });
      setShowForm(false);
      setName(""); setPassword("");
      qc.invalidateQueries({ queryKey: ["sources"] });
    } catch (err) {
      setError(err instanceof Error ? err.message : "registration failed");
    } finally {
      setBusy(false);
    }
  }

  async function testConnection(id: string) {
    setTesting(id);
    try {
      const r = await api.post<{ result: { ok: boolean; message: string } }>(
        `/v1/sources/${id}/test`
      );
      setTestResult((prev) => ({
        ...prev,
        [id]: `${r.result.ok ? "✓" : "✗"} ${r.result.message}`,
      }));
      qc.invalidateQueries({ queryKey: ["sources"] });
    } catch (err) {
      setTestResult((prev) => ({ ...prev, [id]: `✗ ${err instanceof Error ? err.message : "failed"}` }));
    } finally {
      setTesting(null);
    }
  }

  async function discover(id: string) {
    setTesting(id);
    try {
      await api.post(`/v1/sources/${id}/discover`);
      setTestResult((prev) => ({ ...prev, [id]: "Discovery run queued" }));
    } catch (err) {
      setTestResult((prev) => ({ ...prev, [id]: `✗ ${err instanceof Error ? err.message : "failed"}` }));
    } finally {
      setTesting(null);
    }
  }

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <div>
          <h1 style={{ fontSize: 22, fontWeight: 700 }}>Data Sources</h1>
          <p className="text-dim" style={{ fontSize: 13, marginTop: 4 }}>
            Connect the systems DataAtlas discovers and monitors. Credentials are encrypted at rest and never displayed again.
          </p>
        </div>
        <button className="btn btn-primary" onClick={() => setShowForm((v) => !v)}>
          {showForm ? "Cancel" : "Add source"}
        </button>
      </div>

      {showForm && (
        <form className="surface" style={{ padding: 18, display: "grid", gap: 12, maxWidth: 560 }} onSubmit={registerSource}>
          <div style={{ display: "grid", gap: 12, gridTemplateColumns: "1fr 1fr" }}>
            <label style={{ display: "grid", gap: 4, fontSize: 12 }} className="text-dim">
              Name
              <input className="input" value={name} onChange={(e) => setName(e.target.value)} required />
            </label>
            <label style={{ display: "grid", gap: 4, fontSize: 12 }} className="text-dim">
              Connector
              <select className="input" value={connector} onChange={(e) => setConnector(e.target.value)}>
                {CONNECTORS.map((c) => (
                  <option key={c.key} value={c.key}>{c.label}</option>
                ))}
              </select>
            </label>
          </div>
          <div style={{ display: "grid", gap: 12, gridTemplateColumns: "2fr 1fr" }}>
            <label style={{ display: "grid", gap: 4, fontSize: 12 }} className="text-dim">
              Host / Path
              <input className="input" value={host} onChange={(e) => setHost(e.target.value)} required />
            </label>
            <label style={{ display: "grid", gap: 4, fontSize: 12 }} className="text-dim">
              Port
              <input className="input" value={port} onChange={(e) => setPort(e.target.value)} />
            </label>
          </div>
          <div style={{ display: "grid", gap: 12, gridTemplateColumns: "1fr 1fr" }}>
            <label style={{ display: "grid", gap: 4, fontSize: 12 }} className="text-dim">
              Database
              <input className="input" value={database} onChange={(e) => setDatabase(e.target.value)} />
            </label>
            <label style={{ display: "grid", gap: 4, fontSize: 12 }} className="text-dim">
              User
              <input className="input" value={user} onChange={(e) => setUser(e.target.value)} />
            </label>
          </div>
          <label style={{ display: "grid", gap: 4, fontSize: 12 }} className="text-dim">
            Password
            <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
          </label>
          {error && <div className="badge badge-red" style={{ padding: 8 }}>{error}</div>}
          <div>
            <button className="btn btn-primary" type="submit" disabled={busy}>
              {busy ? "Registering…" : "Register source"}
            </button>
          </div>
        </form>
      )}

      <div className="surface" style={{ padding: 6 }}>
        {(sources.data?.sources ?? []).length === 0 ? (
          <p className="text-dim" style={{ padding: 16, fontSize: 13 }}>
            No sources registered yet. Add one to start discovery.
          </p>
        ) : (
          <table className="data">
            <thead>
              <tr>
                <th>Name</th><th>Connector</th><th>Status</th><th>Registered</th><th style={{ width: 260 }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {(sources.data?.sources ?? []).map((s) => (
                <tr key={s.id}>
                  <td style={{ fontWeight: 600 }}>{s.name}</td>
                  <td className="text-dim">{s.connector_key}</td>
                  <td><span className={statusBadgeClass(s.status)}>{s.status}</span></td>
                  <td className="text-dim">{timeAgo(s.created_at)}</td>
                  <td>
                    <div style={{ display: "flex", gap: 6 }}>
                      <button className="btn" style={{ padding: "4px 10px", fontSize: 12 }} disabled={testing === s.id} onClick={() => testConnection(s.id)}>
                        {testing === s.id ? "…" : "Test"}
                      </button>
                      <button className="btn" style={{ padding: "4px 10px", fontSize: 12 }} disabled={testing === s.id} onClick={() => discover(s.id)}>
                        Discover
                      </button>
                    </div>
                    {testResult[s.id] && (
                      <div className="text-dim" style={{ fontSize: 11, marginTop: 4 }}>{testResult[s.id]}</div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
