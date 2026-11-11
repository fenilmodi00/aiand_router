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
import {
  useAiandAuthStatus,
  useMetricsModelBreakdown,
  useMetricsSummary,
  useMetricsTimeseries,
  useOnboardingStatus,
  useSessionMe,
} from "@/lib/apiHooks";
import { cn } from "@/lib/cn";
import { LoadState } from "@/tools/LoadState";
import Link from "next/link";
import { useState } from "react";

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
  const { granularity, range } = dashboardFilters.filters;

  // Set by "Skip to dashboard" / onboarding completion: skips the gate in
  // this tab until reload, alongside the persisted localStorage flag.
  const [skip, setSkip] = useState(false);

  // Upstream-credential state gates the empty-state split and suppresses
  // onboarding while serving is off. Cached under the same key as the app
  // layout's banner probe, so navigating to/from the dashboard never
  // refetches it from scratch.
  const aiandStatus = useAiandAuthStatus();
  const connection: ConnectionState =
    aiandStatus.data == null
      ? "checking"
      : aiandStatus.data.connected
        ? "connected"
        : "disconnected";

  const me = useSessionMe();
  const onboardingStatus = useOnboardingStatus(
    connection === "connected" ? 3_000 : undefined,
  );

  // A router that has never served a request has nothing to chart, so a fresh
  // install lands in onboarding instead of on six empty charts. The gate is
  // the installation-level first_request_served_at, not a key's last_used_at:
  // that flag survives rotation, so rotating the key that served the first
  // request can't send an established install back through onboarding. The
  // skip key is namespaced by installation id, so the me probe supplies it.
  let onboarding: OnboardingState = "checking";
  if (connection === "disconnected") {
    onboarding = "done";
  } else if (me.data != null) {
    const id = me.data.installation?.external_id;
    if (onboardingStatus.data != null) {
      const served = onboardingStatus.data.first_request_served_at != null;
      onboarding =
        served || skip || onboardingSkipped(skipOnboardingKey(id)) ? "done" : "needed";
    }
  } else if (me.error != null) {
    // Non-fatal: on a failed probe show the dashboard rather than trapping
    // an established install in onboarding.
    onboarding = "done";
  }

  function rememberSkip() {
    rememberOnboardingSkipped(skipOnboardingKey(me.data?.installation?.external_id));
  }

  // Metric reads share the SWR cache keyed by range id + granularity:
  // returning to the dashboard renders the previous charts instantly and
  // revalidates in the background (every 30s) instead of flashing skeletons.
  const summary = useMetricsSummary(range.id, granularity);
  const timeseries = useMetricsTimeseries(range.id, granularity);
  const modelBreakdown = useMetricsModelBreakdown(range.id, granularity);

  const metricsError =
    summary.error?.message ??
    timeseries.error?.message ??
    modelBreakdown.error?.message ??
    null;

  const summaryData = onboarding === "done" ? (summary.data ?? null) : null;
  const buckets = onboarding === "done" ? (timeseries.data?.buckets ?? []) : [];
  const modelBuckets = onboarding === "done" ? (modelBreakdown.data?.buckets ?? []) : [];

  if (connection === "checking") return null;
  // No upstream credential: serving is off, so onboarding (which needs a key
  // and a served request) can't progress. Show the disconnected state instead.
  // This must come before the onboarding gate: the onboarding probe returns
  // early while disconnected, leaving its state at "checking" forever.
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
  if (onboarding === "checking") return null;
  if (onboarding === "needed") {
    return (
      <RouterOnboarding
        onComplete={() => setSkip(true)}
        onSkip={() => {
          void rememberSkip();
          setSkip(true);
        }}
      />
    );
  }

  if (metricsError != null) {
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
          <div className="rounded-md border border-danger/30 bg-danger/5 p-4 text-sm text-danger">
            {metricsError}
          </div>
        </Page.Section>
      </Page>
    );
  }

  const savingsRate =
    summaryData == null || summaryData.total_requested_cost_usd === 0
      ? 0
      : (summaryData.total_savings_usd / summaryData.total_requested_cost_usd) * 100;
  const avgTokensPerReq =
    summaryData == null || summaryData.request_count === 0
      ? 0
      : summaryData.total_tokens / summaryData.request_count;
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
            value={summaryData == null ? "—" : formatUSD(Math.abs(summaryData.total_savings_usd))}
            sub={
              summaryData == null
                ? undefined
                : summaryData.total_savings_usd >= 0
                  ? `${savingsRate.toFixed(1)}% of requested`
                  : "Over requested cost"
            }
            accent={
              summaryData == null
                ? "default"
                : summaryData.total_savings_usd >= 0
                  ? "success"
                  : "danger"
            }
          />
          <MetricCard
            className={ResponsiveGrid.Small}
            label="Requests"
            value={summaryData == null ? "—" : formatNumber(summaryData.request_count)}
            sub={
              summaryData == null ? undefined : `actual ${formatUSD(summaryData.total_actual_cost_usd)}`
            }
          />
          <MetricCard
            className={ResponsiveGrid.Small}
            label="Tokens"
            value={summaryData == null ? "—" : formatNumber(summaryData.total_tokens)}
            sub={summaryData == null ? undefined : `${formatNumber(avgTokensPerReq)} avg / req`}
          />

          <ChartCard
            className={ResponsiveGrid.Full}
            title="Router cost savings"
            subtitle="Actual cost vs. what would have been charged for the requested model."
            topRight={
              <Statistic
                statistic={
                  summaryData == null
                    ? LoadState.loading()
                    : LoadState.loaded({
                        total:
                          summaryData.total_savings_usd >= 0
                            ? `${formatUSD(summaryData.total_savings_usd)} saved`
                            : `${formatUSD(Math.abs(summaryData.total_savings_usd))} extra`,
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
  return (
    <Card className={className}>
      <Card.Content>
        <p className="text-2xs font-medium text-muted-foreground">{label}</p>
        <p
          className={cn(
            "mt-2 text-xl font-medium",
            accent === "success" && "text-success",
            accent === "danger" && "text-danger",
          )}
        >
          {value}
        </p>
        {sub != null && <p className="mt-1 text-2xs text-muted-foreground">{sub}</p>}
      </Card.Content>
    </Card>
  );
}

function EmptyChart({ height = 240 }: { height?: number }) {
  return (
    <div className="flex items-center justify-center" style={{ height }}>
      <p className="text-2xs text-muted-foreground">No requests in this period</p>
    </div>
  );
}
