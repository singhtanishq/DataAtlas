"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useState } from "react";
import { api, type DatasetSummary } from "@/lib/api";
import { formatNumber, statusBadgeClass } from "@/components/auth";

export default function CatalogPage() {
  const [query, setQuery] = useState("");
  const [type, setType] = useState("");
  const [offset, setOffset] = useState(0);
  const limit = 25;

  const catalog = useQuery({
    queryKey: ["catalog", query, type, offset],
    queryFn: () =>
      api.get<{ datasets: DatasetSummary[]; total: number }>(
        `/v1/catalog/datasets?q=${encodeURIComponent(query)}&type=${type}&limit=${limit}&offset=${offset}`
      ),
  });

  const total = catalog.data?.total ?? 0;
  const datasets = catalog.data?.datasets ?? [];

  return (
    <div style={{ display: "grid", gap: 18 }}>
      <div>
        <h1 style={{ fontSize: 22, fontWeight: 700 }}>Catalog Explorer</h1>
        <p className="text-dim" style={{ fontSize: 13, marginTop: 4 }}>
          {formatNumber(total)} datasets across your connected sources.
        </p>
      </div>

      <div style={{ display: "flex", gap: 10 }}>
        <input
          className="input"
          style={{ maxWidth: 380 }}
          placeholder="Search datasets, schemas, descriptions…"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setOffset(0);
          }}
        />
        <select
          className="input"
          style={{ maxWidth: 180 }}
          value={type}
          onChange={(e) => {
            setType(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">All types</option>
          <option value="TABLE">Tables</option>
          <option value="VIEW">Views</option>
          <option value="FILE">Files</option>
          <option value="DBT_MODEL">dbt models</option>
          <option value="DBT_SOURCE">dbt sources</option>
          <option value="EXTERNAL">External</option>
        </select>
      </div>

      <div className="surface" style={{ padding: 6 }}>
        {catalog.isLoading ? (
          <p className="text-dim" style={{ padding: 16, fontSize: 13 }}>Loading catalog…</p>
        ) : datasets.length === 0 ? (
          <p className="text-dim" style={{ padding: 16, fontSize: 13 }}>
            No datasets match. Register a source and run discovery, or ingest lineage events.
          </p>
        ) : (
          <table className="data">
            <thead>
              <tr>
                <th>Dataset</th><th>Type</th><th>Source</th><th>Rows</th><th>Columns</th>
              </tr>
            </thead>
            <tbody>
              {datasets.map((d) => (
                <tr key={d.id}>
                  <td>
                    <Link href={`/catalog/${d.id}`} style={{ fontWeight: 600, color: "var(--accent)" }}>
                      {d.full_name}
                    </Link>
                    {d.description && (
                      <div className="text-dim" style={{ fontSize: 12, marginTop: 2 }}>
                        {d.description.slice(0, 90)}
                      </div>
                    )}
                  </td>
                  <td><span className={statusBadgeClass(d.asset_type)}>{d.asset_type.replace("MATERIALIZED_", "")}</span></td>
                  <td className="text-dim">{d.source_name ?? "—"}</td>
                  <td>{formatNumber(d.row_count)}</td>
                  <td>{formatNumber(d.column_count)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center" }}>
        <span className="text-dim" style={{ fontSize: 12 }}>
          Showing {offset + 1}–{Math.min(offset + limit, total)} of {formatNumber(total)}
        </span>
        <div style={{ display: "flex", gap: 8 }}>
          <button className="btn" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - limit))}>
            Previous
          </button>
          <button className="btn" disabled={offset + limit >= total} onClick={() => setOffset(offset + limit)}>
            Next
          </button>
        </div>
      </div>
    </div>
  );
}
