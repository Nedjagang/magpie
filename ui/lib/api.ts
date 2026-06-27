// API base URL resolution.
//
// Deliberately NOT via Next.js rewrites or any server-side proxy. With auth
// disabled, magpied's CORS allowlist (or no auth in v0.1 mode) lets the
// browser call it directly. With v0.2 token auth on, the operator's browser
// holds the bearer token in localStorage and attaches it on every request.
//
// Port 12002 must be reachable from operator browsers. It already is, since
// agents connect to it too. If you put the UI behind a TLS reverse proxy,
// also proxy /api and /v1/opamp at that layer and everything keeps working.
function resolveAPI(): string {
  if (typeof window === "undefined") return ""; // SSR — shouldn't be reached
  const { protocol, hostname, port } = window.location;
  const override = (window as unknown as { __MAGPIE_API?: string }).__MAGPIE_API;
  if (override !== undefined) return override;
  // When served on a standard port (443/80, i.e. behind a reverse proxy),
  // use same-origin so the proxy routes /api/* to magpied.
  // In dev (port 12001), talk directly to magpied on :12002.
  if (!port || port === "443" || port === "80") return "";
  return `${protocol}//${hostname}:12002`;
}

export const API = resolveAPI();

const TOKEN_KEY = "magpie.token";
const ACTOR_KEY = "magpie.actor";

// AuthRequiredError signals that the magpied control plane returned 401.
// Page-level code listens for this and renders the sign-in modal rather
// than passing the error through as a generic toast.
export class AuthRequiredError extends Error {
  constructor(msg = "auth required") {
    super(msg);
    this.name = "AuthRequiredError";
  }
}

export const auth = {
  getToken(): string {
    if (typeof window === "undefined") return "";
    return window.localStorage.getItem(TOKEN_KEY) ?? "";
  },
  setToken(t: string) {
    if (typeof window === "undefined") return;
    if (t) window.localStorage.setItem(TOKEN_KEY, t);
    else window.localStorage.removeItem(TOKEN_KEY);
  },
  getActor(): string {
    if (typeof window === "undefined") return "";
    return window.localStorage.getItem(ACTOR_KEY) ?? "";
  },
  setActor(a: string) {
    if (typeof window === "undefined") return;
    if (a) window.localStorage.setItem(ACTOR_KEY, a);
    else window.localStorage.removeItem(ACTOR_KEY);
  },
};

function authHeaders(): Record<string, string> {
  const out: Record<string, string> = {};
  const t = auth.getToken();
  if (t) out["Authorization"] = `Bearer ${t}`;
  const a = auth.getActor();
  if (a) out["X-Magpie-Actor"] = a;
  return out;
}

// apiFetch is the one chokepoint every request goes through. Centralises
// auth header attachment, 401 normalisation, and error-body parsing — so
// adding a new endpoint can never silently skip auth (the easy mistake
// when each fetch call sets its own headers, which the v0.1 layout did).
async function apiFetch(path: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  for (const [k, v] of Object.entries(authHeaders())) headers.set(k, v);
  const r = await fetch(`${API}${path}`, { ...init, headers, cache: init.cache ?? "no-store" });
  if (r.status === 401) {
    throw new AuthRequiredError();
  }
  return r;
}

async function apiJSON<T>(path: string, init: RequestInit = {}): Promise<T> {
  const r = await apiFetch(path, init);
  return parseJSONOrThrow<T>(r);
}

async function parseJSONOrThrow<T>(r: Response): Promise<T> {
  if (!r.ok) {
    let msg = `${r.status} ${r.statusText}`;
    try {
      const body = (await r.json()) as { error?: string };
      if (body?.error) msg = body.error;
    } catch {
      /* ignore */
    }
    throw new Error(msg);
  }
  return r.json() as Promise<T>;
}

export type Agent = {
  instance_uid: string;
  attributes?: Record<string, string>;
  healthy?: boolean;
  last_status?: string;
  connected_at: string;
  last_seen: string;
  applied_config_hash?: string;
  config_status?: string;
  config_error?: string;
  effective_product?: string;
  effective_variant?: string;
  label_override?: boolean;
  connected: boolean;
};

export type Config = {
  id: number;
  name: string;
  product: string;
  variant: string;
  yaml: string;
  created_at: string;
};

export type AuditEntry = {
  id: number;
  at: string;
  actor: string;
  action: string;
  product?: string;
  variant?: string;
  target_id?: number;
  detail?: string;

  // v1.0 structured fields (per migration 00009 + audit.go RecordEvent).
  // Empty / undefined on legacy v0.2 rows.
  type?: string;
  scope_kind?: string;
  scope_ref?: string;
  config_ref?: number;
  host_ref?: string;
  payload_json?: string;

  // Hash chain. Empty on legacy rows.
  prev_hash?: string;
  hash?: string;
};

// Audit query filter — maps to /api/v1/audit query params per spec §11.2.
// All fields optional; empty/undefined means "no filter on this dim."
export type AuditFilter = {
  host_ref?: string;
  scope_kind?: string;
  scope_ref?: string;
  type?: string;
  actor?: string;
  since?: "1h" | "24h" | "7d" | "30d" | "all";
};

// Audit chain verification status — backed by GET /api/v1/audit/verify.
// brokenAtId is set only when the chain has been tampered with; the row
// at that id is where the validator stopped accepting prev_hash matches.
export type AuditChainStatus = {
  valid: boolean;
  valid_up_to_id: number;
  broken_at_id?: number;
};

export type CreateConfigInput = {
  name: string;
  product: string;
  variant: string;
  yaml: string;
};

// v1.0 Rollout primitive (docs/v1.0-rollout-spec.md). The publish dialog
// posts one of these instead of POST /configs to thread the new Config
// through the canary/soak/promote machinery.
export type ScopeKind = "product_variant" | "instance";

export type RolloutKind = "phased" | "instant";

export type GateMode = "auto" | "manual";

export type CreateRolloutInput = {
  scope_kind: ScopeKind;
  scope_ref: string; // "product/variant" for product_variant; instance_uid for instance
  config: { name: string; yaml: string };
  rollout_kind: RolloutKind;
  canary?: { pct?: number; count?: number };
  soak_seconds?: number;
  gate_mode?: GateMode;
};

export type RolloutState =
  | "validating" | "canary" | "soak" | "promoting"
  | "paused" | "done" | "aborted";

export type Rollout = {
  id: number;
  scope_kind: ScopeKind;
  scope_ref: string;
  config_id: number;
  prior_config_id?: number;
  rollout_kind: RolloutKind;
  state: RolloutState;
  prev_state?: RolloutState;
  canary_pct?: number;
  canary_count?: number;
  canary_size?: number;
  soak_seconds: number;
  gate_mode: GateMode;
  gate_passed_at?: string;
  created_at: string;
  created_by: string;
  validated_at?: string;
  canary_at?: string;
  soak_at?: string;
  promoted_at?: string;
  done_at?: string;
  paused_at?: string;
  aborted_at?: string;
  abort_reason?: string;
};

// ApplyAggregate is the rollup the GET /rollouts/:id endpoint returns
// alongside the Rollout — counts of per-host apply_state rows by state +
// canary/promote split. Drives the publish dialog's live-progress view
// and the Rollouts drawer's phase metrics.
export type ApplyAggregate = {
  pending: number;
  applying: number;
  applied: number;
  failed: number;
  total_canary: number;
  total_promote: number;
};

export type RolloutWithAggregate = {
  rollout: Rollout;
  apply_state_summary: ApplyAggregate;
};

// ApplyState — per-host record for one rollout (matches the schema from
// migration 00008 + the Go ApplyState type). The Rollout drawer's
// failure detail tabs render lists of these.
export type ApplyStateValue = "pending" | "applying" | "applied" | "failed";

export type ApplyState = {
  rollout_id: number;
  instance_uid: string;
  state: ApplyStateValue;
  is_canary: boolean;
  attempt_count: number;
  applied_hash?: string;
  last_error?: string;
  pushed_at?: string;
  updated_at: string;
};

export type RolloutListFilter = {
  scope_kind?: ScopeKind;
  scope_ref?: string;
  inFlight?: boolean;
};

// RolloutInFlightError carries the existing rollout's id when POST /rollouts
// returns 409. The dialog uses this to render the spec's "rollout already
// in flight" banner with a link to the existing rollout.
export class RolloutInFlightError extends Error {
  constructor(public existingRolloutId: number, public existingState: string) {
    super(`rollout already in flight (#${existingRolloutId}, state=${existingState})`);
    this.name = "RolloutInFlightError";
  }
}

// RolloutValidateError carries the validation detail from the server.
// Spec §12: validate failed surfaces with the specific reason ("processor
// X not configured") so the operator can fix without leaving the dialog.
export class RolloutValidateError extends Error {
  constructor(public detail: string) {
    super(detail);
    this.name = "RolloutValidateError";
  }
}

export type ReleasePlatform = {
  os: string;
  arch: string;
  size_bytes: number;
};

export type ReleaseCatalog = {
  version: string;
  platforms: ReleasePlatform[];
};

const jsonHeaders = { "Content-Type": "application/json" };

export const api = {
  agents: () => apiJSON<Agent[]>("/api/v1/agents"),
  configs: (filter?: { product?: string; variant?: string }) => {
    const q = new URLSearchParams();
    if (filter?.product) q.set("product", filter.product);
    if (filter?.variant) q.set("variant", filter.variant);
    const qs = q.toString();
    return apiJSON<Config[]>(`/api/v1/configs${qs ? `?${qs}` : ""}`);
  },
  createConfig: (input: CreateConfigInput) =>
    apiJSON<Config>("/api/v1/configs", {
      method: "POST",
      headers: jsonHeaders,
      body: JSON.stringify(input),
    }),
  rollback: (id: number) =>
    apiJSON<Config>(`/api/v1/configs/${id}/rollback`, { method: "POST" }),
  // v1.0 publish path. Posts to /rollouts so the new config flows through
  // the canary/soak/promote pipeline. Maps the server's typed errors
  // (409 rollout_in_progress, 400 validate_failed) onto JS exception
  // classes the dialog renders inline.
  createRollout: async (input: CreateRolloutInput): Promise<Rollout> => {
    const r = await apiFetch("/api/v1/rollouts", {
      method: "POST",
      headers: jsonHeaders,
      body: JSON.stringify(input),
    });
    if (r.status === 409) {
      const body = (await r.json().catch(() => ({}))) as {
        existing_rollout_id?: number;
        existing_state?: string;
      };
      throw new RolloutInFlightError(body.existing_rollout_id ?? 0, body.existing_state ?? "");
    }
    if (r.status === 400) {
      const body = (await r.json().catch(() => ({}))) as { details?: string; error?: string };
      throw new RolloutValidateError(body.details ?? body.error ?? "validate failed");
    }
    return parseJSONOrThrow<Rollout>(r);
  },
  listRollouts: (filter?: RolloutListFilter) => {
    const q = new URLSearchParams();
    if (filter?.scope_kind) q.set("scope_kind", filter.scope_kind);
    if (filter?.scope_ref) q.set("scope_ref", filter.scope_ref);
    if (filter?.inFlight) q.set("state", "in_flight");
    const qs = q.toString();
    return apiJSON<Rollout[]>(`/api/v1/rollouts${qs ? `?${qs}` : ""}`);
  },
  getRollout: (id: number) =>
    apiJSON<RolloutWithAggregate>(`/api/v1/rollouts/${id}`),
  // listApplyState fetches per-host apply state for a rollout, paginated.
  // hostRef, when set, restricts the result to that single instance_uid —
  // used by the host-drawer in-flight strip so per-poll fetch is one row
  // instead of up to a 5000-row page.
  listApplyState: (id: number, limit = 200, cursor?: string, hostRef?: string) => {
    const q = new URLSearchParams();
    q.set("limit", String(limit));
    if (cursor) q.set("cursor", cursor);
    if (hostRef) q.set("host_ref", hostRef);
    return apiJSON<ApplyState[]>(`/api/v1/rollouts/${id}/apply-state?${q.toString()}`);
  },
  // Operator actions on a Rollout. All five share the same response
  // shape (200 OK with the updated Rollout). 409 happens when the
  // current rollout state doesn't allow the action — rendered inline
  // in the drawer so the operator can correct course.
  pauseRollout: (id: number) =>
    apiJSON<Rollout>(`/api/v1/rollouts/${id}/pause`, { method: "POST" }),
  resumeRollout: (id: number) =>
    apiJSON<Rollout>(`/api/v1/rollouts/${id}/resume`, { method: "POST" }),
  abortRollout: (id: number, reason?: string) =>
    apiJSON<Rollout>(`/api/v1/rollouts/${id}/abort`, {
      method: "POST",
      headers: jsonHeaders,
      body: JSON.stringify(reason ? { reason } : {}),
    }),
  fastPromoteRollout: (id: number) =>
    apiJSON<Rollout>(`/api/v1/rollouts/${id}/fast-promote`, { method: "POST" }),
  promoteRollout: (id: number) =>
    apiJSON<Rollout>(`/api/v1/rollouts/${id}/promote`, { method: "POST" }),
  audit: (limit = 50) =>
    apiJSON<AuditEntry[]>(`/api/v1/audit?limit=${limit}`),
  // listAudit is the v1.0 audit-section query method. Server-side
  // filtering on host_ref/scope/type/actor/since (per spec §11.2). The
  // older `audit(limit)` method stays for v0.2-compat callers; the
  // audit UI section uses this one.
  listAudit: (filter: AuditFilter = {}, limit = 100, cursor?: string) => {
    const q = new URLSearchParams();
    q.set("limit", String(limit));
    if (cursor) q.set("cursor", cursor);
    if (filter.host_ref) q.set("host_ref", filter.host_ref);
    if (filter.scope_kind) q.set("scope_kind", filter.scope_kind);
    if (filter.scope_ref) q.set("scope_ref", filter.scope_ref);
    if (filter.type) q.set("type", filter.type);
    if (filter.actor) q.set("actor", filter.actor);
    if (filter.since && filter.since !== "all") q.set("since", filter.since);
    return apiJSON<AuditEntry[]>(`/api/v1/audit?${q.toString()}`);
  },
  verifyAuditChain: () =>
    apiJSON<AuditChainStatus>("/api/v1/audit/verify"),
  // /healthz is unauthenticated; bypass apiFetch so the auth layer doesn't
  // wrap a 200 from a non-token magpied as AuthRequiredError.
  health: () =>
    fetch(`${API}/healthz`, { cache: "no-store" })
      .then((r) => (r.ok ? (r.json() as Promise<{ status: string; version: string }>) : null))
      .catch(() => null),
  // repushAgent forces magpied to re-send the resolved config to one
  // agent. Used when apply_state has been stuck in "applying" past a
  // reasonable threshold — typically because the prior push was suppressed
  // somewhere in the OpAMP delivery path. Returns 409 if the agent is not
  // currently connected (treat as "wait for the agent to come back").
  repushAgent: (uid: string) =>
    apiJSON<{ sent: boolean }>(`/api/v1/agents/${encodeURIComponent(uid)}/repush`, {
      method: "POST",
    }),
  deleteProduct: (product: string) =>
    apiJSON<{ removed: number }>(`/api/v1/products/${encodeURIComponent(product)}`, {
      method: "DELETE",
    }),
  deleteVariant: (product: string, variant: string) =>
    apiJSON<{ removed: number }>(
      `/api/v1/products/${encodeURIComponent(product)}/variants/${encodeURIComponent(variant)}`,
      { method: "DELETE" },
    ),
  setAgentLabels: async (uid: string, product: string, variant: string) => {
    const r = await apiFetch(`/api/v1/agents/${encodeURIComponent(uid)}/labels`, {
      method: "PUT",
      headers: jsonHeaders,
      body: JSON.stringify({ product, variant }),
    });
    if (!r.ok) {
      let msg = `${r.status} ${r.statusText}`;
      try {
        const b = (await r.json()) as { error?: string };
        if (b?.error) msg = b.error;
      } catch {}
      throw new Error(msg);
    }
  },
  clearAgentLabels: async (uid: string) => {
    const r = await apiFetch(`/api/v1/agents/${encodeURIComponent(uid)}/labels`, {
      method: "DELETE",
    });
    if (!r.ok) throw new Error(`${r.status} ${r.statusText}`);
  },
  deleteAgent: async (uid: string) => {
    const r = await apiFetch(`/api/v1/agents/${encodeURIComponent(uid)}`, {
      method: "DELETE",
    });
    if (!r.ok) {
      let msg = `${r.status} ${r.statusText}`;
      try {
        const b = (await r.json()) as { error?: string };
        if (b?.error) msg = b.error;
      } catch {}
      throw new Error(msg);
    }
  },
  releases: () => apiJSON<ReleaseCatalog>("/api/v1/releases"),
  // downloadReleaseZip pulls the agent zip via fetch+Blob rather than a
  // plain <a href> so the bearer token can ride on the request. Trades
  // streaming for ~10 MB of browser memory; agents fall into that easily.
  // Returns a Blob the caller can save via createObjectURL.
  downloadReleaseZip: async (os: string, arch: string) => {
    const r = await apiFetch(`/api/v1/releases/${encodeURIComponent(os)}/${encodeURIComponent(arch)}`);
    if (!r.ok) throw new Error(`${r.status} ${r.statusText}`);
    return r.blob();
  },
};
