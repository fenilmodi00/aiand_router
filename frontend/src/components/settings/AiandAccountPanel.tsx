"use client";

import { ErrorBanner, EmptyHint, formatDate } from "./shared";
import { Text } from "@/components/atoms/Text";
import { Button } from "@/components/molecules/Button";
import { Card } from "@/components/molecules/Card";
import { Appearance, Intent } from "@/components/types";
import { api, type AiandAuthStatus } from "@/lib/api";
import Link from "next/link";
import { useEffect, useState } from "react";

type View = "loading" | "connected" | "disconnected";

// Line items on the connected card, built once so the layout stays a plain
// list rather than field-by-field JSX.
function statusRows(status: AiandAuthStatus): { label: string; value: string }[] {
  const rows: { label: string; value: string }[] = [];
  if (status.email != null) rows.push({ label: "Account", value: status.email });
  if (status.org_name != null) rows.push({ label: "Organization", value: status.org_name });
  if (status.expires_at != null) rows.push({ label: "Key expires", value: formatDate(status.expires_at) });
  rows.push({
    label: "Connected via",
    value: status.origin === "device" ? "Device sign-in" : "Pasted API key",
  });
  return rows;
}

export function AiandAccountPanel() {
  const [view, setView] = useState<View>("loading");
  const [status, setStatus] = useState<AiandAuthStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  // The panel mounts on the landing from /settings/providers?connect=1; only
  // the first load opens the inline confirm card.
  const [confirming, setConfirming] = useState(false);

  useEffect(() => {
    let cancelled = false;
    api.aiandAuth
      .status()
      .then(s => {
        if (cancelled) return;
        setStatus(s);
        setView(s.connected ? "connected" : "disconnected");
        if (s.connected) {
          const params = new URLSearchParams(window.location.search);
          if (params.get("connect") === "1") setConfirming(true);
        }
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load Aiand account status.");
        setView("disconnected");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function handleDisconnect() {
    setDisconnecting(true);
    setError(null);
    try {
      await api.aiandAuth.disconnect();
      setStatus(null);
      setConfirming(false);
      setView("disconnected");
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to disconnect.");
    } finally {
      setDisconnecting(false);
    }
  }

  if (view === "loading") {
    return (
      <Card className="p-0">
        <Card.Content>
          <div className="px-5 py-8 text-center text-2xs text-muted-foreground">Loading…</div>
        </Card.Content>
      </Card>
    );
  }

  if (view === "connected" && status?.connected) {
    return (
      <>
        {error != null && <ErrorBanner>{error}</ErrorBanner>}
        <Card className="p-0">
          <Card.Header className="flex-row items-center justify-between gap-3 border-b border-border px-5 py-3">
            <Card.Title variant="h4">Aiand account</Card.Title>
            <Button
              appearance={Appearance.Outlined}
              intent={Intent.Danger}
              size="sm"
              aria-expanded={confirming}
              onClick={() => setConfirming(c => !c)}
              disabled={disconnecting}
            >
              Disconnect
            </Button>
          </Card.Header>
          <Card.Content className="p-0">
            <dl className="flex flex-col divide-y divide-border">
              {statusRows(status).map(row => (
                <div key={row.label} className="flex items-center justify-between gap-3 px-5 py-3">
                  <dt className="text-xs text-muted-foreground">{row.label}</dt>
                  <dd className="truncate font-mono text-2xs text-foreground">{row.value}</dd>
                </div>
              ))}
            </dl>
            <Text className="block border-t border-border px-5 py-3 text-2xs text-muted-foreground">
              Disconnecting stops routing, but this dashboard session stays signed in — you can
              reconnect at any time without signing in again.
            </Text>
            {confirming && (
              <div
                role="alertdialog"
                aria-labelledby="aiand-disconnect-title"
                className="flex flex-col gap-3 border-t border-danger/30 bg-danger/5 px-5 py-4"
              >
                <Text id="aiand-disconnect-title" className="text-xs font-semibold text-danger">
                  Disconnect your Aiand account?
                </Text>
                <Text className="text-2xs text-foreground">
                  Disconnecting stops routing immediately. Your router keys stay valid, but requests
                  will fail until you reconnect.
                </Text>
                <div className="flex items-center gap-2">
                  <Button
                    appearance={Appearance.Filled}
                    intent={Intent.Danger}
                    size="sm"
                    onClick={() => void handleDisconnect()}
                    disabled={disconnecting}
                  >
                    {disconnecting ? "Disconnecting…" : "Disconnect"}
                  </Button>
                  <Button
                    type="button"
                    appearance={Appearance.Hollow}
                    className="text-xs text-muted-foreground"
                    onClick={() => setConfirming(false)}
                    disabled={disconnecting}
                  >
                    Cancel
                  </Button>
                </div>
              </div>
            )}
          </Card.Content>
        </Card>
      </>
    );
  }

  return (
    <>
      {error != null && <ErrorBanner>{error}</ErrorBanner>}
      <EmptyHint>
        <div className="flex flex-col items-center gap-3">
          <Text className="text-xs font-medium text-foreground">
            Aiand is disconnected. Requests to this router are failing — reconnect to start serving
            again.
          </Text>
          <Text className="text-2xs text-muted-foreground">
            Reconnecting signs you in through the Aiand device flow (or a pasted API key). Your
            dashboard session is unaffected.
          </Text>
          <div className="flex flex-wrap items-center justify-center gap-2">
            <Button
              href="/login"
              appearance={Appearance.Filled}
              intent={Intent.Primary}
              className="!border-brand !bg-brand !text-white hover:!bg-brand/90"
            >
              Reconnect via Aiand sign-in
            </Button>
            <Link
              href="/login?mode=paste"
              className="text-2xs text-muted-foreground underline underline-offset-2 hover:text-foreground"
            >
              Paste an API key instead
            </Link>
          </div>
        </div>
      </EmptyHint>
    </>
  );
}
