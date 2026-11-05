"use client";

import { useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { api, type Identity } from "@/lib/api";

export function useIdentity() {
  const [identity, setIdentity] = useState<Identity | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api
      .get<{ user: Identity }>("/v1/auth/me")
      .then((r) => setIdentity(r.user))
      .catch(() => setIdentity(null))
      .finally(() => setLoading(false));
  }, []);

  const logout = useCallback(async () => {
    await api.post("/v1/auth/logout").catch(() => undefined);
    setIdentity(null);
  }, []);

  return { identity, loading, logout };
}

export function useRequireIdentity() {
  const { identity, loading, logout } = useIdentity();
  const router = useRouter();
  useEffect(() => {
    if (!loading && !identity) {
      router.replace("/login");
    }
  }, [loading, identity, router]);
  return { identity, loading, logout };
}

export function statusBadgeClass(status: string): string {
  const s = status.toUpperCase();
  if (["ACTIVE", "SUCCEEDED", "PASSED", "OK", "FRESH", "HEALTHY", "PUBLISHED"].includes(s))
    return "badge badge-green";
  if (["FAILED", "ERROR", "STALE", "DISCONNECTED"].includes(s)) return "badge badge-red";
  if (["RUNNING", "PENDING", "WARNING", "VALIDATING", "CONFIGURED"].includes(s))
    return "badge badge-amber";
  if (["OPEN", "ACKNOWLEDGED"].includes(s)) return "badge badge-red";
  return "badge badge-gray";
}

export function formatNumber(n?: number | null): string {
  if (n === undefined || n === null) return "—";
  return new Intl.NumberFormat().format(n);
}

export function formatDuration(ms?: number | null): string {
  if (ms === undefined || ms === null) return "—";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`;
}

export function formatAge(seconds?: number | null): string {
  if (seconds === undefined || seconds === null) return "—";
  if (seconds < 90) return `${Math.round(seconds)}s ago`;
  if (seconds < 5400) return `${Math.round(seconds / 60)}m ago`;
  if (seconds < 172_800) return `${Math.round(seconds / 3600)}h ago`;
  return `${Math.round(seconds / 86400)}d ago`;
}

export function timeAgo(iso?: string | null): string {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "—";
  return formatAge((Date.now() - then) / 1000);
}
