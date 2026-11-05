"use client";

import { EmptyHint, ErrorBanner, formatDate } from "./shared";
import { Text } from "@/components/atoms/Text";
import { Input } from "@/components/Input";
import { Button } from "@/components/molecules/Button";
import { Card } from "@/components/molecules/Card";
import { Appearance, Intent } from "@/components/types";
import {
  api,
  SlowDownError,
  type AiandAuthStatus,
  type AiandDeviceStartResponse,
} from "@/lib/api";
import { Check, Copy, ExternalLink, Loader2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";

// Device-flow polls never fire faster than every 2s, even if the backend
// reports a shorter interval: a burst of polls is worse than a slower
// handshake, and the backend applies slow_down itself. In seconds, matching
// the start response's `interval` unit.
const MIN_POLL_INTERVAL_S = 2;

type View = "loading" | "connected" | "disconnected" | "device" | "paste";

// Line items on the connected card, built once so the layout stays a plain
// list rather than field-by-field JSX.
function statusRows(status: AiandAuthStatus): { label: string; value: string }[] {
  const rows: { label: string; value: string }[] = [];
  if (status.email != null) rows.push({ label: "Account", value: status.email });
  if (status.org_name != null) rows.push({ label: "Organization", value: status.org_name });
  if (status.key_masked != null) rows.push({ label: "API key", value: status.key_masked });
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
  const [device, setDevice] = useState<AiandDeviceStartResponse | null>(null);
  const [pollMessage, setPollMessage] = useState<string | null>(null);
  const [pasteKey, setPasteKey] = useState("");
  const [pasteError, setPasteError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const [copied, setCopied] = useState(false);
  // Guards every poll response: bumped per attempt and again at effect
  // teardown, so a straggler response can never clobber newer state.
  const pollSeqRef = useRef(0);
  const deviceRef = useRef<AiandDeviceStartResponse | null>(null);
  deviceRef.current = device;

  useEffect(() => {
    let cancelled = false;
    api.aiandAuth
      .status()
      .then(s => {
        if (cancelled) return;
        setStatus(s);
        setView(s.connected ? "connected" : "disconnected");
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

  // Refs, not state, so closing over them in the polling effect stays stable
  // across renders: the timeout handle, the mutable next-interval, and the
  // cancellation flag outlive the effect's closure.
  const cancelledPollRef = useRef(false);
  const intervalMsRef = useRef(0);
  useEffect(() => {
    if (view !== "device" || device == null) return;
    cancelledPollRef.current = false;
    intervalMsRef.current = Math.max(device.interval, MIN_POLL_INTERVAL_S) * 1000;
    pollSeqRef.current += 1;
    const seq = pollSeqRef.current;

    function poll() {
      api.aiandAuth
        .pollDevice()
        .then(res => {
          // A response for a superseded attempt or after teardown must not
          // touch state.
          if (cancelledPollRef.current || seq !== pollSeqRef.current) return;
          if (res.status === "authorized") {
            setStatus({
              connected: true,
              email: res.email,
              org_name: res.org_name,
              key_masked: res.key_masked,
              origin: "device",
            });
            setView("connected");
            return;
          }
          if (res.status === "pending") {
            timer = window.setTimeout(poll, intervalMsRef.current);
            return;
          }
          // expired / denied / error: terminal for this attempt. Map to a
          // single actionable message; the backend owns the distinction.
          setPollMessage(
            res.status === "expired"
              ? "The sign-in code expired."
              : res.status === "denied"
                ? "Sign-in was denied."
                : "Sign-in failed. Please try again.",
          );
        })
        .catch((err: unknown) => {
          if (cancelledPollRef.current || seq !== pollSeqRef.current) return;
          // A 428 slow_down response is not a failure: wait longer (the
          // Retry-After value when provided, otherwise double the interval)
          // and keep polling. A straggler 428 after teardown or a newer
          // attempt only touches intervalMsRef, which the next poll cycle
          // resets from the start response.
          if (err instanceof SlowDownError) {
            const retryMs =
              err.retry_after_s == null
                ? intervalMsRef.current * 2
                : Math.max(err.retry_after_s * 1000, MIN_POLL_INTERVAL_S * 1000);
            intervalMsRef.current = Math.max(intervalMsRef.current, retryMs);
            timer = window.setTimeout(poll, intervalMsRef.current);
            return;
          }
          setPollMessage(err instanceof Error ? err.message : "Failed to check sign-in status.");
        });
    }

    let timer = window.setTimeout(poll, intervalMsRef.current);
    return () => {
      cancelledPollRef.current = true;
      window.clearTimeout(timer);
      pollSeqRef.current += 1;
    };
  }, [view, device]);

  function startDeviceFlow() {
    setError(null);
    setPollMessage(null);
    api.aiandAuth
      .startDevice()
      .then(start => {
        setDevice(start);
        setView("device");
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : "Failed to start sign-in.");
      });
  }

  function handleCopyCode() {
    const code = deviceRef.current?.user_code;
    if (code == null) return;
    navigator.clipboard.writeText(code).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  }

  async function handlePasteSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (pasteKey.trim() === "") return;
    setSubmitting(true);
    setPasteError(null);
    try {
      const res = await api.aiandAuth.pasteKey(pasteKey.trim());
      setStatus({
        connected: true,
        email: res.email,
        org_name: res.org_name,
        key_masked: res.key_masked,
        origin: "paste",
      });
      setView("connected");
      setPasteKey("");
    } catch (err: unknown) {
      // request() flattens non-2xx responses to "401: {body}"; a 401 here
      // means the key was rejected.
      setPasteError(
        err instanceof Error && err.message.startsWith("401")
          ? "Invalid key."
          : err instanceof Error
            ? err.message
            : "Failed to save key.",
      );
    } finally {
      setSubmitting(false);
    }
  }

  async function handleDisconnect() {
    const confirmed = window.confirm(
      "Disconnect your Aiand account?\n\nRequests will stop routing until you sign in again.",
    );
    if (!confirmed) return;
    setDisconnecting(true);
    setError(null);
    try {
      await api.aiandAuth.disconnect();
      // Any in-flight device attempt belongs to the old session; drop it.
      setDevice(null);
      setPollMessage(null);
      setPasteKey("");
      setStatus(null);
      setView("disconnected");
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Failed to disconnect.");
    } finally {
      setDisconnecting(false);
    }
  }

  function tryAgain() {
    setPollMessage(null);
    setDevice(null);
    setView("disconnected");
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
              size="sm"
              onClick={() => void handleDisconnect()}
              disabled={disconnecting}
            >
              {disconnecting ? "Disconnecting…" : "Disconnect"}
            </Button>
          </Card.Header>
          <Card.Content>
            <dl className="flex flex-col divide-y divide-border">
              {statusRows(status).map(row => (
                <div key={row.label} className="flex items-center justify-between gap-3 px-5 py-3">
                  <dt className="text-xs text-muted-foreground">{row.label}</dt>
                  <dd className="truncate font-mono text-2xs text-foreground">{row.value}</dd>
                </div>
              ))}
            </dl>
          </Card.Content>
        </Card>
      </>
    );
  }

  if (view === "device" && device != null) {
    return (
      <Card>
        <Card.Header>
          <Card.Title variant="h4">Sign in to Aiand</Card.Title>
          <Card.Description>
            Approve the sign-in in your browser — this page connects on its own once you do.
          </Card.Description>
        </Card.Header>
        <Card.Content className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Text className="text-xs font-medium text-foreground">
              Enter this code at{" "}
              <a
                href={device.verification_uri}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 text-brand underline"
              >
                {device.verification_uri}
                <ExternalLink className="size-3" />
              </a>
            </Text>
            <div className="flex items-center gap-2">
              <code className="flex-1 rounded bg-muted px-3 py-2 text-center font-mono text-base font-semibold tracking-widest text-foreground">
                {device.user_code}
              </code>
              <Button appearance={Appearance.Outlined} size="sm" onClick={handleCopyCode}>
                {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
                {copied ? "Copied" : "Copy"}
              </Button>
            </div>
          </div>

          {pollMessage != null ? (
            <div className="flex flex-col items-start gap-2">
              <ErrorBanner>{pollMessage}</ErrorBanner>
              <Button appearance={Appearance.Outlined} size="sm" onClick={tryAgain}>
                Try again
              </Button>
            </div>
          ) : (
            <div className="flex items-center gap-2 text-muted-foreground">
              <Loader2 className="size-4 animate-spin text-brand" />
              <Text className="text-2xs">Waiting for approval…</Text>
            </div>
          )}

          <button
            className="self-start text-2xs text-muted-foreground underline"
            onClick={tryAgain}
          >
            Cancel
          </button>
        </Card.Content>
      </Card>
    );
  }

  if (view === "paste") {
    return (
      <>
        {error != null && <ErrorBanner>{error}</ErrorBanner>}
        <Card>
          <Card.Header>
            <Card.Title variant="h4">Paste your Aiand API key</Card.Title>
            <Card.Description>
              Create a key in your Aiand account settings and paste it here.
            </Card.Description>
          </Card.Header>
          <Card.Content>
            <form onSubmit={handlePasteSubmit} className="flex flex-col gap-3" autoComplete="off">
              <Input
                label="API key"
                name="aiand-api-key"
                type="password"
                autoComplete="off"
                data-1p-ignore
                data-lpignore="true"
                data-form-type="other"
                placeholder="sk-…"
                value={pasteKey}
                onChange={e => setPasteKey(e.target.value)}
              />
              {pasteError != null && <ErrorBanner>{pasteError}</ErrorBanner>}
              <div className="flex items-center gap-2">
                <Button
                  type="submit"
                  appearance={Appearance.Filled}
                  intent={Intent.Primary}
                  className="!border-brand !bg-brand !text-white hover:!bg-brand/90"
                  disabled={submitting || pasteKey.trim() === ""}
                >
                  {submitting ? "Saving…" : "Save key"}
                </Button>
                <Button
                  type="button"
                  appearance={Appearance.Hollow}
                  className="text-xs text-muted-foreground"
                  onClick={() => {
                    setView("disconnected");
                    setPasteError(null);
                    setPasteKey("");
                  }}
                >
                  Back
                </Button>
              </div>
            </form>
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
            Connect your Aiand account to start routing.
          </Text>
          <div className="flex flex-wrap items-center justify-center gap-2">
            <Button
              appearance={Appearance.Filled}
              intent={Intent.Primary}
              className="!border-brand !bg-brand !text-white hover:!bg-brand/90"
              onClick={startDeviceFlow}
            >
              Sign in with Aiand
            </Button>
            <Button
              appearance={Appearance.Outlined}
              size="sm"
              onClick={() => {
                setPasteError(null);
                setView("paste");
              }}
            >
              Paste API key instead
            </Button>
          </div>
        </div>
      </EmptyHint>
    </>
  );
}
