"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import {
  Activity, BarChart3, Bell, BookOpen, Database, FileSearch,
  GitBranch, LayoutDashboard, Plug, ShieldCheck,
} from "lucide-react";
import { useRequireIdentity } from "@/components/auth";
import type { Identity } from "@/lib/api";

const NAV = [
  { section: "Overview", items: [{ href: "/", label: "Dashboard", icon: LayoutDashboard }] },
  {
    section: "Discover",
    items: [
      { href: "/sources", label: "Data Sources", icon: Plug },
      { href: "/catalog", label: "Catalog Explorer", icon: FileSearch },
    ],
  },
  {
    section: "Intelligence",
    items: [{ href: "/lineage", label: "Data Lineage", icon: GitBranch }],
  },
  {
    section: "Quality & Observability",
    items: [
      { href: "/quality", label: "Quality Checks", icon: ShieldCheck },
      { href: "/jobs", label: "Pipeline Runs", icon: Activity },
      { href: "/freshness", label: "Freshness Monitor", icon: BarChart3 },
      { href: "/alerts", label: "Alerts", icon: Bell },
    ],
  },
  {
    section: "Governance",
    items: [
      { href: "/glossary", label: "Business Glossary", icon: BookOpen },
      { href: "/audit", label: "Audit Logs", icon: Database },
    ],
  },
];

export default function DashboardLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const { identity, loading, logout } = useRequireIdentity();
  const pathname = usePathname();
  const router = useRouter();

  if (loading) {
    return (
      <div style={{ minHeight: "100vh", display: "grid", placeItems: "center" }}>
        <span className="text-dim">Loading DataAtlas…</span>
      </div>
    );
  }
  if (!identity) return null;

  return (
    <div style={{ display: "flex", minHeight: "100vh" }}>
      <aside
        style={{
          width: 232,
          borderRight: "1px solid var(--border)",
          background: "var(--surface)",
          padding: "16px 12px",
          position: "sticky",
          top: 0,
          height: "100vh",
          overflowY: "auto",
          flexShrink: 0,
        }}
      >
        <Link href="/" style={{ display: "flex", alignItems: "center", gap: 9, padding: "4px 8px 16px" }}>
          <div
            style={{
              width: 30, height: 30, borderRadius: 8, background: "var(--accent)",
              display: "grid", placeItems: "center", fontWeight: 800, fontSize: 15, color: "#fff",
            }}
          >
            DA
          </div>
          <span style={{ fontWeight: 700, fontSize: 16 }}>DataAtlas</span>
        </Link>
        {NAV.map((group) => (
          <div key={group.section} style={{ marginBottom: 14 }}>
            <div
              className="text-dim"
              style={{
                fontSize: 10, textTransform: "uppercase", letterSpacing: "0.08em",
                fontWeight: 700, padding: "6px 10px",
              }}
            >
              {group.section}
            </div>
            {group.items.map((item) => {
              const active =
                item.href === "/" ? pathname === "/" : pathname.startsWith(item.href);
              const Icon = item.icon;
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  style={{
                    display: "flex", alignItems: "center", gap: 9,
                    padding: "7px 10px", borderRadius: 7, fontSize: 13,
                    fontWeight: 500, marginBottom: 1,
                    background: active ? "var(--accent-soft)" : "transparent",
                    color: active ? "var(--accent)" : "var(--text)",
                  }}
                >
                  <Icon size={15} strokeWidth={2} />
                  {item.label}
                </Link>
              );
            })}
          </div>
        ))}
      </aside>

      <div style={{ flex: 1, minWidth: 0 }}>
        <header
          style={{
            display: "flex", alignItems: "center", justifyContent: "space-between",
            padding: "10px 24px", borderBottom: "1px solid var(--border)",
            background: "var(--surface)", position: "sticky", top: 0, zIndex: 10,
          }}
        >
          <div className="text-dim" style={{ fontSize: 12 }}>
            {identity.organization_name}
          </div>
          <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
            <span className="badge badge-blue">{identity.role}</span>
            <span style={{ fontSize: 13 }}>{identity.display_name}</span>
            <button
              className="btn"
              style={{ padding: "4px 10px", fontSize: 12 }}
              onClick={async () => {
                await logout();
                router.replace("/login");
              }}
            >
              Sign out
            </button>
          </div>
        </header>
        <main style={{ padding: 24, maxWidth: 1280 }}>{children}</main>
      </div>
    </div>
  );
}
