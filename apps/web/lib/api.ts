"use client";

// Typed API client for the DataAtlas control plane. Authentication flows
// through the HTTP-only session cookie set by the control plane.

const BASE_URL =
  process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";

export class ApiError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown
): Promise<T> {
  const response = await fetch(`${BASE_URL}${path}`, {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "include",
  });
  let payload: unknown = null;
  try {
    payload = await response.json();
  } catch {
    // empty body
  }
  if (!response.ok) {
    const err = (payload as { error?: { code?: string; message?: string } })?.error;
    throw new ApiError(
      response.status,
      err?.code ?? "unknown",
      err?.message ?? `request failed: ${response.status}`
    );
  }
  return payload as T;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body),
  patch: <T>(path: string, body?: unknown) => request<T>("PATCH", path, body),
  delete: <T>(path: string) => request<T>("DELETE", path),
};

// -- shared types ------------------------------------------------------

export interface Identity {
  user_id: string;
  email: string;
  display_name: string;
  organization_id: string;
  organization_name: string;
  role: string;
}

export interface DatasetSummary {
  id: string;
  asset_id: string;
  namespace: string;
  name: string;
  full_name: string;
  asset_type: string;
  source_name?: string;
  description?: string;
  row_count?: number;
  column_count?: number;
}

export interface DatasetDetails extends DatasetSummary {
  database_name?: string;
  schema_name?: string;
  row_count_method?: string;
  current_schema_version?: number;
  current_version_is_breaking?: boolean;
  owners: Owner[];
  tags: Tag[];
}

export interface Owner {
  assignment_id: string;
  ownership_role: string;
  owner_type: string;
  user_name?: string;
  team_name?: string;
  assigned_at: string;
}

export interface Tag {
  id: string;
  name: string;
  color?: string;
}

export interface Column {
  id: string;
  name: string;
  ordinal: number;
  data_type: string;
  nullable: boolean;
  is_primary_key: boolean;
  is_foreign_key: boolean;
  fk_reference?: string;
  description?: string;
}

export interface Source {
  id: string;
  name: string;
  slug: string;
  status: string;
  connector_key: string;
  config: Record<string, unknown>;
  created_at: string;
}

export interface JobSummary {
  id: string;
  namespace: string;
  name: string;
  scheduler?: string;
  status: string;
  total_runs: number;
  successful_runs: number;
  failed_runs: number;
  last_run_at?: string;
  last_status?: string;
  avg_duration_ms?: number;
}

export interface QualityCheck {
  id: string;
  dataset_id: string;
  name: string;
  check_type: string;
  target_column?: string;
  severity: string;
  enabled: boolean;
}

export interface GlossaryTerm {
  id: string;
  name: string;
  definition: string;
  category?: string;
  synonyms: string[];
  status: string;
}

export interface Alert {
  id: string;
  alert_type: string;
  severity: string;
  title: string;
  summary?: string;
  status: string;
  created_at: string;
}

export interface AuditEntry {
  id: number;
  actor_user_id?: string;
  actor_kind: string;
  action: string;
  entity_type: string;
  entity_id?: string;
  created_at: string;
}

export interface FreshnessStatus {
  dataset_id: string;
  full_name: string;
  asset_type: string;
  status: string;
  age_seconds?: number;
  expected_interval_seconds?: number;
}

export interface GraphView {
  root_asset_id: string;
  direction: string;
  nodes: {
    asset_id: string;
    namespace: string;
    name: string;
    full_name: string;
    asset_type: string;
    depth: number;
  }[];
  edges: {
    source_asset_id: string;
    target_asset_id: string;
    job_name?: string;
    origin: string;
    confidence: string;
  }[];
}
