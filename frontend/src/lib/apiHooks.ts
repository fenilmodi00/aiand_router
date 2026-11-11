"use client";

import { DATE_RANGES, type Granularity } from "@/components/DashboardPageFilters/useDashboardFilters";
import { api, type AiandAuthStatus, type APIKey, type DeployedModel, type ExcludedModelsResponse, type MetricsSummary, type MeResponse, type OnboardingStatus, type RouterConfig, type TimeseriesBucket, type ModelBreakdownBucket, type RoutingPreferencesResponse } from "@/lib/api";
import useSWR, { mutate } from "swr";

/**
 * Stable SWR keys — the shared cache identity across every page. Metric keys
 * are keyed by range id + granularity, never by the computed from/to
 * timestamps (those change on every evaluation and would defeat cache hits);
 * the fetcher recomputes the window at each revalidation instead.
 */
export const swrKeys = {
  me: ["auth", "me"],
  aiandAuthStatus: ["aiand-auth", "status"],
  onboarding: ["onboarding"],
  config: ["config"],
  keys: ["keys"],
  excludedModels: ["excluded-models"],
  routingPreferences: ["routing-preferences"],
  metricsSummary: (rangeId: string, granularity: Granularity) =>
    ["metrics", "summary", rangeId, granularity] as const,
  metricsTimeseries: (rangeId: string, granularity: Granularity) =>
    ["metrics", "timeseries", rangeId, granularity] as const,
  metricsModelBreakdown: (rangeId: string, granularity: Granularity) =>
    ["metrics", "model-breakdown", rangeId, granularity] as const,
};

function rangeWindow(rangeId: string): { fromISO: string; toISO: string } {
  const to = new Date();
  const range = DATE_RANGES.find(r => r.id === rangeId) ?? DATE_RANGES[0];
  const from = range.start(to);
  return { fromISO: from.toISOString(), toISO: to.toISOString() };
}

/** Cached session identity (GET /account/v1/auth/me). */
export function useSessionMe() {
  return useSWR<MeResponse>(swrKeys.me, () => api.auth.me());
}

/** Cached upstream-credential status; `pollMs` adds a background refresh interval. */
export function useAiandAuthStatus(pollMs?: number) {
  return useSWR<AiandAuthStatus>(
    swrKeys.aiandAuthStatus,
    () => api.aiandAuth.status(),
    pollMs != null ? { refreshInterval: pollMs } : undefined,
  );
}

/** Cached installation onboarding status; `pollMs` adds a background refresh interval. */
export function useOnboardingStatus(pollMs?: number) {
  return useSWR<OnboardingStatus>(
    swrKeys.onboarding,
    () => api.onboarding.get(),
    pollMs != null ? { refreshInterval: pollMs } : undefined,
  );
}

/** Cached runtime router configuration. */
export function useConfig() {
  return useSWR<RouterConfig>(swrKeys.config, () => api.config.get());
}

/** Cached router key list, shared by the keys panel and onboarding. */
export function useKeys() {
  return useSWR<{ keys: APIKey[] }>(swrKeys.keys, () => api.keys.list());
}

/** Cached available/excluded model list for the model-selection panel. */
export function useExcludedModels() {
  return useSWR<ExcludedModelsResponse>(swrKeys.excludedModels, () =>
    api.excludedModels.get(),
  );
}

/** Cached quality/price routing preferences. */
export function useRoutingPreferences() {
  return useSWR<RoutingPreferencesResponse>(swrKeys.routingPreferences, () =>
    api.routingPreferences.get(),
  );
}

// Metric hooks refresh in the background every 30s so a dashboard left open
// stays current without ever flashing a loading state (keepPreviousData).

/** Cached metrics summary for a date range, keyed by range id + granularity. */
export function useMetricsSummary(rangeId: string, granularity: Granularity) {
  return useSWR<MetricsSummary>(
    swrKeys.metricsSummary(rangeId, granularity),
    () => {
      const { fromISO, toISO } = rangeWindow(rangeId);
      return api.metrics.summary(fromISO, toISO);
    },
    { refreshInterval: 30_000, keepPreviousData: true },
  );
}

/** Cached request/cost timeseries for a date range, keyed by range id + granularity. */
export function useMetricsTimeseries(rangeId: string, granularity: Granularity) {
  return useSWR<{ buckets: TimeseriesBucket[] }>(
    swrKeys.metricsTimeseries(rangeId, granularity),
    () => {
      const { fromISO, toISO } = rangeWindow(rangeId);
      return api.metrics.timeseries(granularity, fromISO, toISO);
    },
    { refreshInterval: 30_000, keepPreviousData: true },
  );
}

/** Cached per-model usage breakdown, keyed by range id + granularity. */
export function useMetricsModelBreakdown(rangeId: string, granularity: Granularity) {
  return useSWR<{ buckets: ModelBreakdownBucket[] }>(
    swrKeys.metricsModelBreakdown(rangeId, granularity),
    () => {
      const { fromISO, toISO } = rangeWindow(rangeId);
      return api.metrics.modelBreakdown(granularity, fromISO, toISO);
    },
    { refreshInterval: 30_000, keepPreviousData: true },
  );
}

/** Revalidates the router key list after issue/rotate/delete mutations. */
export function invalidateKeys() {
  return mutate(swrKeys.keys);
}

/** Revalidates the model-selection list after a save. */
export function invalidateExcludedModels() {
  return mutate(swrKeys.excludedModels);
}

/** Revalidates routing preferences after a save or reset. */
export function invalidateRoutingPreferences() {
  return mutate(swrKeys.routingPreferences);
}

/** Revalidates upstream-credential status after connect/disconnect. */
export function invalidateAiandAuthStatus() {
  return mutate(swrKeys.aiandAuthStatus);
}

/** Revalidates the session identity after login/logout. */
export function invalidateMe() {
  return mutate(swrKeys.me);
}

/** Revalidates the installation onboarding status after the first served request. */
export function invalidateOnboarding() {
  return mutate(swrKeys.onboarding);
}

export type { DeployedModel };
