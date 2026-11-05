"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, type GlossaryTerm } from "@/lib/api";
import { statusBadgeClass, useRequireIdentity } from "@/components/auth";

export default function GlossaryPage() {
  useRequireIdentity();
  const qc = useQueryClient();
  const terms = useQuery({
    queryKey: ["glossary"],
    queryFn: () => api.get<{ terms: GlossaryTerm[] }>("/v1/glossary/terms"),
  });
  const [showForm, setShowForm] = useState(false);
  const [name, setName] = useState("");
  const [definition, setDefinition] = useState("");
  const [category, setCategory] = useState("");
  const [error, setError] = useState<string | null>(null);

  async function createTerm(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      await api.post("/v1/glossary/terms", {
        name, definition, category: category || undefined,
      });
      setName(""); setDefinition(""); setCategory("");
      setShowForm(false);
      qc.invalidateQueries({ queryKey: ["glossary"] });
    } catch (err) {
      setError(err instanceof Error ? err.message : "creation failed");
    }
  }

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <div>
          <h1 style={{ fontSize: 22, fontWeight: 700 }}>Business Glossary</h1>
          <p className="text-dim" style={{ fontSize: 13, marginTop: 4 }}>
            Standardized business terminology with definitions and ownership.
          </p>
        </div>
        <button className="btn btn-primary" onClick={() => setShowForm((v) => !v)}>
          {showForm ? "Cancel" : "New term"}
        </button>
      </div>

      {showForm && (
        <form className="surface" style={{ padding: 18, display: "grid", gap: 12, maxWidth: 560 }} onSubmit={createTerm}>
          <label className="text-dim" style={{ display: "grid", gap: 4, fontSize: 12 }}>
            Term
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} required />
          </label>
          <label className="text-dim" style={{ display: "grid", gap: 4, fontSize: 12 }}>
            Definition
            <textarea className="input" rows={3} value={definition} onChange={(e) => setDefinition(e.target.value)} required />
          </label>
          <label className="text-dim" style={{ display: "grid", gap: 4, fontSize: 12 }}>
            Category (optional)
            <input className="input" value={category} onChange={(e) => setCategory(e.target.value)} />
          </label>
          {error && <div className="badge badge-red" style={{ padding: 8 }}>{error}</div>}
          <div><button className="btn btn-primary" type="submit">Create term</button></div>
        </form>
      )}

      <div style={{ display: "grid", gridTemplateColumns: "repeat(2, 1fr)", gap: 14 }}>
        {(terms.data?.terms ?? []).map((t) => (
          <div key={t.id} className="surface" style={{ padding: 16 }}>
            <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 6 }}>
              <h3 style={{ fontSize: 14, fontWeight: 700 }}>{t.name}</h3>
              <span className={statusBadgeClass(t.status)}>{t.status}</span>
            </div>
            <p style={{ fontSize: 13, color: "var(--text)" }}>{t.definition}</p>
            {t.category && <div className="text-dim" style={{ fontSize: 12, marginTop: 6 }}>Category: {t.category}</div>}
          </div>
        ))}
      </div>
      {(terms.data?.terms ?? []).length === 0 && !terms.isLoading && (
        <div className="surface" style={{ padding: 24 }}>
          <p className="text-dim" style={{ fontSize: 13 }}>No terms yet. Define the language of your business here.</p>
        </div>
      )}
    </div>
  );
}
