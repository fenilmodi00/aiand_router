"use client";

import { Text } from "@/components/atoms/Text";
import { ChartCard } from "@/components/ChartCard";
import { CostBreakdownChart } from "@/components/charts/CostBreakdownChart";
import { CumulativeSavingsChart } from "@/components/charts/CumulativeSavingsChart";
import { ModelBreakdownChart } from "@/components/charts/ModelBreakdownChart";
import { RouterCostSavingsChart } from "@/components/charts/RouterCostSavingsChart";
import { SavingsRateChart } from "@/components/charts/SavingsRateChart";
import {
  DashboardPageFilters,
  useDashboardFilters,
} from "@/components/DashboardPageFilters";
import { Card } from "@/components/molecules/Card";
import { Page } from "@/components/Page";
import { PageHeader } from "@/components/PageHeader";
import { ResponsiveGrid } from "@/components/ResponsiveGrid";
import { RouterOnboarding } from "@/components/RouterOnboarding";
import { Statistic } from "@/components/Statistic";
import { api, type MetricsSummary, type ModelBreakdownBucket, type TimeseriesBucket } from "@/lib/api";
import { cn } from "@/lib/cn";
import { LoadState } from "@/tools/LoadState";
import Link from "next/link";
import { useEffect, useState } from "react";

function formatUSD(v: number): string {
  if (v === 0) return "$0.00";
  if (Math.abs(v) < 0.001) return `$${v.toFixed(4)}`;
  return `$${v.toFixed(2)}`;
}

function formatNumber(v: number): string {
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`;
  return String(v);
}

// "checking" suppresses a flash of either surface until the onboarding probe
// lands.
type OnboardingState = "checking" | "needed" | "done";

// Connection state from aiandAuth.status(): "checking" until the first probe
// lands, then the live upstream-credential state.
type ConnectionState = "checking" | "connected" | "disconnected";

// Set when the user chooses "Skip to dashboard". Persisted rather than held in
// memory so a refresh doesn't drop them back into a flow they opted out of;
// the server-side flag still takes over for good once a request is served.
// Namespaced by installation id so a shared browser doesn't leak the skip
// between workspaces — an installation without an id (shouldn't happen while
// authed) gets its own key rather than falling back to a global one.
const SKIP_ONBOARDING_PREFIX = "aiand-router.onboarding-skipped:";

function skipOnboardingKey(externalId: string | undefined): string {
  return externalId == null ? `${SKIP_ONBOARDING_PREFIX}default` : `${SKIP_ONBOARDING_PREFIX}${externalId}`;
}

function onboardingSkipped(key: string): boolean {
  if (typeof window === "undefined") return false;
  try {
    return window.localStorage.getItem(key) === "true";
  } catch {
    // Private-mode/blocked storage: treat as not skipped rather than throwing
    // on the render path.
    return false;
  }
}

function rememberOnboardingSkipped(key: string) {
  try {
    window.localStorage.setItem(key, "true");
  } catch {
    // Non-fatal: the skip just won't survive this reload.
  }
}

export default function DashboardPage() {
  const dashboardFilters = useDashboardFilters("30d");
  const { fromISO, toISO, granularity, range } = dashboardFilters.filters;

  const [summary, setSummary] = useState<MetricsSummary | null>(null);
  const [buckets, setBuckets] = useState<TimeseriesBucket[]>([]);
  const [modelBuckets, setModelBuckets] = useState<ModelBreakdownBucket[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [onboarding, setOnboarding] = useState<OnboardingState>("checking");
  const [connection, setConnection] = useState<ConnectionState>("checking");
  // Installation id that namespaces the localStorage skip key; captured by the
  // me()/onboarding probe so the skip button persists under the same key the
  // gate read.
  const [externalId, setExternalId] = useState<string | undefined>(undefined);

  // Upstream-credential state gates the empty-state split and suppresses
  // onboarding while serving is off.
  useEffect(() => {
    let cancelled = false;
    api.aiandAuth
      .status()
      .then(s => {
        if (cancelled) return;
        setConnection(s.connected ? "connected" : "disconnected");
      })
      // Non-fatal: a failed probe shows the dashboard rather than the
      // disconnected state.
      .catch(() => {
        if (!cancelled) setConnection("connected");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // A router that has never served a request has nothing to chart, so a fresh
  // install lands in onboarding instead of on six empty charts. The gate is the
  // installation-level first_request_served_at, not a key's last_used_at:
  // that flag survives rotation, so rotating the key that served the first
  // request can't send an established install back through onboarding. The
  // skip key is namespaced by installation id, so the probe supplies it.
  useEffect(() => {
    if (connection === "disconnected") return;
    let cancelled = false;
    api.auth
      .me()
      .then(me => {
        if (cancelled) return;
        setExternalId(me.installation?.external_id);
        return api.onboarding.get().then(res => {
          if (cancelled) return;
          const served = res.first_request_served_at != null;
          setOnboarding(
            served || onboardingSkipped(skipOnboardingKey(me.installation?.external_id))
              ? "done"
              : "needed",
          );
        });
      })
      // Non-fatal: on a failed probe show the dashboard rather than trapping
      // an established install in onboarding.
      .catch(() => {
        if (!cancelled) setOnboarding("done");
      });
    return () => {
      cancelled = true;
    };
  }, [connection]);

  function rememberSkip() {
    rememberOnboardingSkipped(skipOnboardingKey(externalId));
  }

  useEffect(() => {
    if (onboarding !== "done") return;
    let cancelled = false;
    setError(null);
    Promise.all([
      api.metrics.summary(fromISO, toISO),
      api.metrics.timeseries(granularity, fromISO, toISO),
      api.metrics.modelBreakdown(granularity, fromISO, toISO),
    ])
      .then(([s, ts, mb]) => {
        if (cancelled) return;
        setSummary(s);
        setBuckets(ts.buckets ?? []);
        setModelBuckets(mb.buckets ?? []);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load metrics.");
      });
    return () => {
      cancelled = true;
    };
  }, [fromISO, toISO, granularity, onboarding]);

  if (onboarding === "checking" || connection === "checking") return null;
  // No upstream credential: serving is off, so onboarding (which needs a key
  // and a served request) can't progress. Show the disconnected state instead.
  if (connection === "disconnected") {
    return (
      <Page
        header={
          <PageHeader
            left={
              <Text
                variant="h4"
                as="h2"
                className="flex flex-row items-center gap-1 whitespace-nowrap"
              >
                Dashboard
              </Text>
            }
          />
        }
      >
        <Page.Section>
          <div className="rounded-lg border-2 border-dashed border-border-darker px-5 py-8 text-center">
            <Text className="text-xs font-medium text-foreground">Serving is off</Text>
            <Text className="mt-2 text-2xs text-muted-foreground">
              Aiand is disconnected, so requests to this router are failing. Reconnect your account
              to start serving again.
            </Text>
            <Link
              href="/account"
              className="mt-4 inline-block rounded-md bg-brand px-4 py-2 text-xs font-medium text-white hover:opacity-90"
            >
              Reconnect
            </Link>
          </div>
        </Page.Section>
      </Page>
    );
  }
  if (onboarding === "needed") {
    return (
      <RouterOnboarding
        onComplete={() => setOnboarding("done")}
        onSkip={() => {
          void rememberSkip();
          setOnboarding("done");
        }}
      />
    );
  }

  if (error) {
    return (
      <Page
        header={
          <PageHeader
            left={
              <Text
                variant="h4"
                as="h2"
                className="flex flex-row items-center gap-1 whitespace-nowrap"
              >
                Dashboard
              </Text>
            }
          />
        }
      >
        <Page.Section>
          <div className="rounded-lg border border-danger/30 bg-danger/5 p-4 text-sm text-danger">
            {error}
          </div>
        </Page.Section>
      </Page>
    );
  }

  const savingsRate =
    summary == null || summary.total_requested_cost_usd === 0
      ? 0
      : (summary.total_savings_usd / summary.total_requested_cost_usd) * 100;
  const avgTokensPerReq =
    summary == null || summary.request_count === 0
      ? 0
      : summary.total_tokens / summary.request_count;
  const empty = buckets.length === 0;
  const modelEmpty = modelBuckets.length === 0;

  return (
    <Page
      header={
        <PageHeader
          left={
            <Text
              variant="h4"
              as="h2"
              className="flex flex-row items-center gap-1 whitespace-nowrap"
            >
              Router cost &amp; savings
            </Text>
          }
        />
      }
    >
      <Page.Section>
        <DashboardPageFilters result={dashboardFilters} />
        <ResponsiveGrid>
          <MetricCard
            className={ResponsiveGrid.Small}
            label="Cost saved"
            value={summary == null ? "—" : formatUSD(Math.abs(summary.total_savings_usd))}
            sub={
              summary == null
                ? undefined
                : summary.total_savings_usd >= 0
                  ? `${savingsRate.toFixed(1)}% of requested`
                  : "Over requested cost"
            }
            accent={
              summary == null
                ? "default"
                : summary.total_savings_usd >= 0
                  ? "success"
                  : "danger"
            }
          />
          <MetricCard
            className={ResponsiveGrid.Small}
            label="Requests"
            value={summary == null ? "—" : formatNumber(summary.request_count)}
            sub={
              summary == null ? undefined : `actual ${formatUSD(summary.total_actual_cost_usd)}`
            }
          />
          <MetricCard
            className={ResponsiveGrid.Small}
            label="Tokens"
            value={summary == null ? "—" : formatNumber(summary.total_tokens)}
            sub={summary == null ? undefined : `${formatNumber(avgTokensPerReq)} avg / req`}
          />

          <ChartCard
            className={ResponsiveGrid.Full}
            title="Router cost savings"
            subtitle="Actual cost vs. what would have been charged for the requested model."
            topRight={
              <Statistic
                statistic={
                  summary == null
                    ? LoadState.loading()
                    : LoadState.loaded({
                        total:
                          summary.total_savings_usd >= 0
                            ? `${formatUSD(summary.total_savings_usd)} saved`
                            : `${formatUSD(Math.abs(summary.total_savings_usd))} extra`,
                      })
                }
              />
            }
          >
            {empty ? (
              <EmptyChart />
            ) : (
              <RouterCostSavingsChart buckets={buckets} granularity={granularity} />
            )}
          </ChartCard>

          <ChartCard
            className={ResponsiveGrid.Medium}
            title="Cumulative savings"
            subtitle={`Running total of dollars saved across the ${range.label.toLowerCase()}.`}
          >
            {empty ? (
              <EmptyChart height={220} />
            ) : (
              <CumulativeSavingsChart buckets={buckets} granularity={granularity} />
            )}
          </ChartCard>

          <ChartCard
            className={ResponsiveGrid.Medium}
            title="Savings rate"
            subtitle="Percent of requested cost avoided per bucket."
          >
            {empty ? (
              <EmptyChart height={200} />
            ) : (
              <SavingsRateChart buckets={buckets} granularity={granularity} />
            )}
          </ChartCard>

          <ChartCard
            className={ResponsiveGrid.Full}
            title="Cost breakdown per bucket"
            subtitle="Actual cost stacked with realized savings."
          >
            {empty ? (
              <EmptyChart height={220} />
            ) : (
              <CostBreakdownChart buckets={buckets} granularity={granularity} />
            )}
          </ChartCard>

          <ChartCard
            className={ResponsiveGrid.Medium}
            title="Model usage"
            subtitle="Requests per bucket broken down by the model the router selected."
          >
            {modelEmpty ? (
              <EmptyChart height={220} />
            ) : (
              <ModelBreakdownChart
                buckets={modelBuckets}
                granularity={granularity}
                metric="requests"
              />
            )}
          </ChartCard>

          <ChartCard
            className={ResponsiveGrid.Medium}
            title="Model spend"
            subtitle="Actual cost per bucket broken down by the model the router selected."
          >
            {modelEmpty ? (
              <EmptyChart height={220} />
            ) : (
              <ModelBreakdownChart
                buckets={modelBuckets}
                granularity={granularity}
                metric="spend"
              />
            )}
          </ChartCard>
        </ResponsiveGrid>
      </Page.Section>
    </Page>
  );
}

interface MetricCardProps {
  className?: string;
  label: string;
  value: string;
  sub?: string;
  accent?: "default" | "success" | "danger";
}

function MetricCard({ className, label, value, sub, accent = "default" }: MetricCardProps) {
  const accentClass =
    accent === "success"
      ? "text-success"
      : accent === "danger"
        ? "text-danger"
        : "text-foreground";

  return (
    <Card size="sm" className={className}>
      <Card.Header>
        <Text className="text-2xs font-medium uppercase tracking-wider text-muted-foreground">
          {label}
        </Text>
      </Card.Header>
      <Card.Content>
        <Text
          className={cn(
            "font-sans text-2xl font-semibold tabular-nums tracking-tight",
            accentClass,
          )}
        >
          {value}
        </Text>
        {sub != null && (
          <Text className="mt-1 text-2xs text-muted-foreground">{sub}</Text>
        )}
      </Card.Content>
    </Card>
  );
}

function EmptyChart({ height = 240 }: { height?: number }) {
  return (
    <div
      className="flex items-center justify-center text-2xs text-muted-foreground"
      style={{ height }}
    >
      No data for this period
    </div>
  );
}
