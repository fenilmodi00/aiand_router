// Session/login surface (device flow, pasted key, me, logout). Deliberately
// unauthenticated by design — the first-ever visitor bootstraps here — and
// backed by the router_account_session cookie.
const ACCOUNT_BASE = "/account/v1";
// Dashboard settings/metrics surface, backed by the admin session cookie.
const ADMIN_BASE = "/admin/v1";

/**
 * HTTP 428 from a device-flow poll: the backend is telling us to slow down,
 * not that polling failed. `retrySeconds` mirrors the Retry-After header in
 * seconds, or null when absent/unparseable (caller then applies its own
 * backoff floor).
 */
export class SlowDownError extends Error {
  retrySeconds: number | null;
  constructor(retrySeconds: number | null) {
    super("slow_down");
    this.name = "SlowDownError";
    this.retrySeconds = retrySeconds;
  }
}

interface RequestOptions extends RequestInit {
  /**
   * False on every login-path call (device start/poll, paste, me, logout) so
   * a 401 surfaces to the caller instead of bouncing the browser to the
   * login page — a redirect here would destroy in-flight device-flow state.
   * me() itself answers "am I logged in?" with 200 {authenticated:false}, so
   * it never sees a 401 in the first place and never redirects either way.
   */
  authRedirect?: boolean;
}

async function rawRequest<T>(base: string, path: string, init?: RequestOptions): Promise<T> {
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...((init?.headers as Record<string, string>) ?? {}),
  };
  const res = await fetch(`${base}${path}`, {
    ...init,
    credentials: "include",
    headers,
  });
  if (res.status === 401 && (init?.authRedirect ?? true) && typeof window !== "undefined") {
    // Session expired or never authenticated — bounce to login. The
    // (app) layout's auth gate handles the same case on initial load,
    // but in-flight calls (chart refreshes, mutations) need their own
    // bounce so users don't sit on a broken page.
    if (!window.location.pathname.startsWith("/ui/login")) {
      // Strip the /ui basePath so Next's router.replace() doesn't re-prepend
      // it after login (which would yield /ui/ui/...). Anything outside the
      // basePath isn't an app path; default to /dashboard.
      const path = window.location.pathname;
      const internal = path.startsWith("/ui/") ? path.slice(3) : "/dashboard";
      const next = encodeURIComponent(internal);
      window.location.href = `/ui/login?next=${next}`;
      throw new Error("401: redirecting to login");
    }
  }
  if (!res.ok) {
    const body = await res.text();
    // The device-flow poll endpoint signals slow_down with HTTP 428 and a
    // Retry-After header instead of a status body, so callers can keep
    // waiting rather than treat it as a failure. The value is in seconds per
    // HTTP spec; absent/invalid falls back to null and the caller applies
    // its own minimum.
    if (res.status === 428) {
      const retryAfter = res.headers.get("Retry-After");
      const seconds = retryAfter == null ? NaN : Number(retryAfter.trim());
      throw new SlowDownError(Number.isFinite(seconds) && seconds >= 0 ? seconds : null);
    }
    throw new Error(`${res.status}: ${body}`);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

// Login-path request: never 401-bounces, whatever the backend answers.
function accountRequest<T>(path: string, init?: RequestOptions): Promise<T> {
  return rawRequest<T>(ACCOUNT_BASE, path, { ...init, authRedirect: false });
}

// Settings/metrics request against /admin/v1. Default 401 behavior: bounce
// to the login page (login-path calls use accountRequest instead).
function request<T>(path: string, init?: RequestOptions): Promise<T> {
  return rawRequest<T>(ADMIN_BASE, path, init);
}

export interface MetricsSummary {
  request_count: number;
  total_tokens: number;
  total_requested_cost_usd: number;
  total_actual_cost_usd: number;
  total_savings_usd: number;
}

export interface TimeseriesBucket {
  bucket: string;
  requested_cost_usd: number;
  actual_cost_usd: number;
}

export interface MetricsTimeseries {
  buckets: TimeseriesBucket[];
}

export interface ModelBreakdownBucket {
  bucket: string;
  decision_model: string;
  request_count: number;
  total_tokens: number;
  actual_cost_usd: number;
}

export interface MetricsModelBreakdown {
  buckets: ModelBreakdownBucket[];
}

export interface MetricsDetailRow {
  timestamp: string;
  request_id: string;
  requested_model: string;
  decision_model: string;
  decision_provider: string;
  decision_reason: string;
  sticky_hit: boolean;
  input_tokens: number;
  output_tokens: number;
  cache_creation_tokens: number | null;
  cache_read_tokens: number | null;
  requested_cost_usd: number;
  actual_cost_usd: number;
  total_latency_ms: number;
  upstream_status_code: number;
  router_user_id: string;
  client_app: string;
  turn_type: string;
  user_email: string;
}

export interface MetricsDetails {
  rows: MetricsDetailRow[];
}

// "routing" keys (rk_) proxy inference; "analytics_read" keys (ra_) are
// read-only export credentials that cannot route or spend.
export type APIKeyScope = "routing" | "analytics_read";

export interface APIKey {
  id: string;
  name: string | null;
  key_prefix: string;
  key_suffix: string;
  scope: APIKeyScope;
  last_used_at: string | null;
  created_at: string;
}

export interface IssueAPIKeyResponse {
  key: APIKey;
  token: string;
}


export interface RouterConfig {
  cluster_version: string;
  embed_only_user_message: boolean;
  otel_enabled: boolean;
  semantic_cache_enabled: boolean;
  env_provider_keys: string[];
}

// Whether this installation has ever served a routed request. Set once and
// never cleared, so it survives key rotation — the dashboard gates first-run
// onboarding on this rather than on a key's last_used_at.
export interface OnboardingStatus {
  first_request_served_at: string | null;
}

// Session state from GET /account/v1/auth/me. Never 401s: the backend
// answers 200 {authenticated:false} when there is no live session, so this
// call is safe from the login page and can never trip the 401 bounce.
export interface MeResponse {
  authenticated: boolean;
  email?: string;
  org_name?: string;
  installation?: {
    external_id: string;
  };
}

export interface DeployedModel {
  model: string;
  provider: string;
}

export interface ExcludedModelsResponse {
  available: DeployedModel[];
  excluded: string[];
  env_override_active: boolean;
}

// Connection between this router installation and the Aiand account that
// supplies the upstream provider key. The raw key is never exposed here —
// only a masked fingerprint.
export interface AiandAuthStatus {
  connected: boolean;
  email?: string;
  org_name?: string;
  key_masked?: string;
  expires_at?: string;
  // The backend sends this as an untyped string (device | paste today);
  // typed as string so unknown future values still render.
  origin?: string;
}

// GET (device flow) — device start/poll/paste now answer with the account
// identity directly; the panel's own AiandAuthStatus view stays on
// /admin/v1/aiand-auth/status.

/** Response of POST /account/v1/auth/device/start. */
export interface AiandDeviceStartResponse {
  login_id: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete: string;
  /** Seconds until the user code expires. */
  expires_in: number;
  /** Seconds to wait between polls; the backend already applies slow_down. */
  interval: number;
}

export type AiandDevicePollStatus = "pending" | "authorized" | "expired" | "denied" | "error";

/** Response of POST /account/v1/auth/device/poll (body of a 200, never a 428). */
export interface AiandDevicePollResponse {
  status: AiandDevicePollStatus;
  email?: string;
  org_name?: string;
}

/** Response of POST /account/v1/auth/paste. */
export interface AiandPasteKeyResponse {
  email: string;
  org_name: string;
}

/**
 * Response of POST /account/v1/auth/browser/start. The backend derives the
 * provider redirect_uri from the request Origin, preflights the authorize
 * URL, and sets the short-lived router_browser_login cookie that the
 * exchange step validates state against.
 */
export interface AiandBrowserStartResponse {
  authorize_url: string;
  /** Seconds until the pending browser sign-in (and its cookie) expires. */
  expires_in: number;
}

/** Response of POST /account/v1/auth/browser/exchange. */
export interface AiandBrowserExchangeResponse {
  email?: string;
  org_name?: string;
  /** Internal path to land on after sign-in (e.g. /dashboard). */
  next: string;
}

export interface AiandDisconnectResponse {
  disconnected: boolean;
}

export interface RoutingPreferencesResponse {
  quality: number;
  price: number;
  is_default: boolean;
}

export const api = {
  auth: {
    // All five of these run against /account/v1/auth/* and never 401-bounce:
    // me/logout answer the login page itself, and start/poll/paste would
    // lose in-flight device state to a redirect. A dead session surfaces to
    // the caller as a rejected promise, not as navigation.
    me: () => accountRequest<MeResponse>("/auth/me"),
    startDeviceLogin: () => accountRequest<AiandDeviceStartResponse>("/auth/device/start", { method: "POST" }),
    // One poll against the gateway. A 428 slow_down throws SlowDownError
    // (Retry-After seconds on it); 404 login_not_found / 502
    // gateway_unavailable / 500 poll_failed reject with plain Errors the
    // caller can branch on by status prefix.
    pollDeviceLogin: (login_id: string) =>
      accountRequest<AiandDevicePollResponse>("/auth/device/poll", {
        method: "POST",
        body: JSON.stringify({ login_id }),
      }),
    // 401 here means a rejected key (invalid_key), not a dead session — the
    // login page must see the rejection to render "Invalid key.", never a
    // redirect. 502 gateway_unavailable rejects with a plain Error too.
    pasteKey: (key: string) =>
      accountRequest<AiandPasteKeyResponse>("/auth/paste", {
        method: "POST",
        body: JSON.stringify({ key }),
      }),
    // Browser (authorization-code + PKCE) sign-in. The backend derives the
    // redirect_uri from the request Origin and preflights the provider
    // authorize URL; 501 {browser_unsupported} tells the login page to fall
    // back to the device flow. Login-path: never 401-bounces.
    startBrowserLogin: (next: string) =>
      accountRequest<AiandBrowserStartResponse>("/auth/browser/start", {
        method: "POST",
        body: JSON.stringify({ next }),
      }),
    // Exchanges the provider's ?code&state redirect for the session cookie,
    // validating state against the router_browser_login cookie. The root
    // page calls this from the bare origin callback.
    exchangeBrowserCode: (code: string, state: string) =>
      accountRequest<AiandBrowserExchangeResponse>("/auth/browser/exchange", {
        method: "POST",
        body: JSON.stringify({ code, state }),
      }),
    logout: () => accountRequest<{ ok: boolean }>("/auth/logout", { method: "POST" }),
  },
  metrics: {
    summary: (from?: string, to?: string) => {
      const params = new URLSearchParams();
      if (from) params.set("from", from);
      if (to) params.set("to", to);
      const qs = params.toString();
      return request<MetricsSummary>(`/metrics/summary${qs ? `?${qs}` : ""}`);
    },
    timeseries: (granularity: "hour" | "day" | "week", from?: string, to?: string) => {
      const params = new URLSearchParams({ granularity });
      if (from) params.set("from", from);
      if (to) params.set("to", to);
      return request<MetricsTimeseries>(`/metrics/timeseries?${params.toString()}`);
    },
    details: (from: string, to: string, limit: number = 100) => {
      const params = new URLSearchParams({ from, to, limit: String(limit) });
      return request<MetricsDetails>(`/metrics/details?${params.toString()}`);
    },
    modelBreakdown: (granularity: "hour" | "day" | "week", from?: string, to?: string) => {
      const params = new URLSearchParams({ granularity });
      if (from) params.set("from", from);
      if (to) params.set("to", to);
      return request<MetricsModelBreakdown>(`/metrics/model-breakdown?${params.toString()}`);
    },
  },
  keys: {
    list: () => request<{ keys: APIKey[] }>("/keys"),
    issue: (name?: string, scope: APIKeyScope = "routing") =>
      request<IssueAPIKeyResponse>("/keys", {
        method: "POST",
        body: JSON.stringify({ name: name ?? "", scope }),
      }),
    // Soft-deletes the named key and issues a replacement in one round-trip.
    // The previous token stops working immediately. Carries forward the
    // previous key's name server-side.
    rotate: (id: string) =>
      request<IssueAPIKeyResponse>(`/keys/${id}/rotate`, { method: "POST" }),
    delete: (id: string) => request<void>(`/keys/${id}`, { method: "DELETE" }),
  },
  config: {
    get: () => request<RouterConfig>("/config"),
  },
  onboarding: {
    get: () => request<OnboardingStatus>("/onboarding"),
  },
  excludedModels: {
    get: () => request<ExcludedModelsResponse>("/excluded-models"),
    update: (excluded: string[]) =>
      request<ExcludedModelsResponse>("/excluded-models", {
        method: "PUT",
        body: JSON.stringify({ excluded }),
      }),
  },
  aiandAuth: {
    status: () => request<AiandAuthStatus>("/aiand-auth/status"),
    disconnect: () =>
      request<AiandDisconnectResponse>("/aiand-auth/disconnect", { method: "POST" }),
    // Device start/poll/paste moved to the /account/v1 session surface —
    // see api.auth.startDeviceLogin/pollDeviceLogin/pasteKey above.
  },
  routingPreferences: {
    get: () => request<RoutingPreferencesResponse>("/routing-preferences"),
    update: (body: { quality: number; price: number }) =>
      request<RoutingPreferencesResponse>("/routing-preferences", {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    reset: () =>
      request<RoutingPreferencesResponse>("/routing-preferences", {
        method: "PUT",
        body: JSON.stringify({ reset: true }),
      }),
  },
};
