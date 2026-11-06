"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";

import { Skeleton } from "@/components/atoms/Skeleton";
import { Input } from "@/components/Input";
import { Button } from "@/components/molecules/Button";
import { Appearance, Intent } from "@/components/types";
import { api, AiandDeviceStartResponse, SlowDownError } from "@/lib/api";

export default function LoginPage() {
  return (
    <Suspense fallback={null}>
      <LoginInner />
    </Suspense>
  );
}

// All login-page copy in one flat table, keyed by scenario. No credential
// form from the legacy flow remains: device sign-in is the primary surface,
// a pasted Aiand key the fallback.
const LOGIN_COPY = {
  title: "Signing in to Aiand Router",
  subhead: "Signing in creates your own isolated router workspace",
  sessionExpiredNote: "Your session expired. Sign in again to continue.",
  starting: "Starting sign-in…",
  startFailed: "Couldn't start sign-in.",
  startTooMany: "Too many attempts — retry in",
  retry: "Retry",
  device: {
    codeLabel: "Enter this code in Aiand to approve sign-in",
    copy: "Copy code",
    copied: "Copied",
    open: "Open Aiand to approve",
    manualFallback: "or enter the code at the Aiand device page:",
    manualFallbackCta: "open the Aiand device page",
    cancel: "Cancel",
    waiting: "Waiting for approval…",
    waitingLong: "Still waiting for approval…",
    expired: "This code has expired.",
    denied: "Sign-in was denied in Aiand.",
    attemptLost: "This sign-in attempt is no longer valid.",
    restart: "Get a new code",
    expiresPrefix: "Code expires in",
    oneMinuteLeft: "Your code expires in less than a minute.",
  },
  paste: {
    switchIn: "Use an API key instead",
    switchBack: "Sign in with a code instead",
    intro: "Paste an Aiand API key (sk-…) to sign in.",
    fieldLabel: "Aiand API key",
    placeholder: "sk-…",
    submit: "Sign in",
    invalidKey: "That key was rejected. Check it and try again.",
    gatewayDown: "Aiand is unreachable right now.",
    unexpected: "Sign-in failed. Try again.",
    tooMany: "Too many attempts — retry in",
  },
  authorizing: "Setting up your workspace…",
} as const;

/**
 * Reject anything that isn't a same-origin internal path (e.g. `javascript:`,
 * `//attacker.com`, `https://...`). After basePath stripping in api.ts the
 * value should look like `/dashboard` or `/settings?tab=keys`.
 */
function safeNext(raw: string | null): string {
  if (raw == null) return "/dashboard";
  if (!raw.startsWith("/")) return "/dashboard";
  if (raw.startsWith("//") || raw.startsWith("/\\")) return "/dashboard";
  return raw;
}

// sessionStorage key for the in-flight device attempt: one JSON value with
// everything needed to resume polling after a reload without minting a new
// code (login_id + start metadata + absolute expiry).
const ATTEMPT_KEY = "aiand-router-login-attempt";
// Cumulative pending-poll time after which the waiting line escalates.
const WAITING_LONG_MS = 15_000;
// Fallback poll delay when a slow-down carries no Retry-After seconds.
const SLOW_DOWN_FALLBACK_S = 5;
// Fixed countdown offered after a 429 (the backend sends no Retry-After body).
const RETRY_WINDOW_S = 60;
// Countdown tick granularity for retry windows.
const RETRY_TICK_MS = 1_000;
// 0–500ms of jitter so reloads/restarts across clients never poll in lockstep.
const POLL_JITTER_MS = 500;
// How long the copied confirmation stays visible.
const COPIED_RESET_MS = 2_000;
// Small paint window so the authorizing skeleton is visible before navigation.
const NAVIGATE_DELAY_MS = 400;

interface StoredAttempt {
  login_id: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete: string;
  /** Absolute epoch ms when the user code expires. */
  expires_at: number;
  /** Seconds between polls, as handed out by the start response. */
  interval: number;
}

function loadAttempt(): StoredAttempt | null {
  try {
    const raw = window.sessionStorage.getItem(ATTEMPT_KEY);
    if (raw == null) return null;
    const parsed = JSON.parse(raw) as StoredAttempt;
    if (
      typeof parsed.login_id !== "string" ||
      typeof parsed.user_code !== "string" ||
      typeof parsed.verification_uri !== "string" ||
      typeof parsed.verification_uri_complete !== "string" ||
      typeof parsed.expires_at !== "number" ||
      typeof parsed.interval !== "number" ||
      parsed.interval <= 0 ||
      parsed.expires_at <= Date.now()
    ) {
      window.sessionStorage.removeItem(ATTEMPT_KEY);
      return null;
    }
    return parsed;
  } catch {
    window.sessionStorage.removeItem(ATTEMPT_KEY);
    return null;
  }
}

function saveAttempt(start: AiandDeviceStartResponse): StoredAttempt {
  const attempt: StoredAttempt = {
    login_id: start.login_id,
    user_code: start.user_code,
    verification_uri: start.verification_uri,
    verification_uri_complete: start.verification_uri_complete,
    expires_at: Date.now() + start.expires_in * 1000,
    interval: start.interval,
  };
  try {
    window.sessionStorage.setItem(ATTEMPT_KEY, JSON.stringify(attempt));
  } catch {
    // Storage may be unavailable (private mode); the flow still works,
    // it just won't survive a reload.
  }
  return attempt;
}

function clearAttempt() {
  try {
    window.sessionStorage.removeItem(ATTEMPT_KEY);
  } catch {
    // Same tolerance as saveAttempt.
  }
}

function formatCountdown(totalSeconds: number): string {
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

// Awaitable sleep that can't keep the loop alive past cancellation: the
// caller re-checks the run token after every await.
function delay(ms: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  window.setTimeout(resolve, ms);
  return promise;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : "";
}

type Phase = "starting" | "device" | "paste" | "authorizing";
type DeviceOutcome = "expired" | "denied" | "lost";

function LoginInner() {
  const router = useRouter();
  const params = useSearchParams();
  const next = safeNext(params.get("next"));
  const deepLinkPaste = params.get("mode") === "paste";
  const sessionExpired = params.get("reason") === "expired";

  const [phase, setPhase] = useState<Phase>("starting");
  // Device-start failure. Null = nothing to show; a string with the live
  // retry countdown renders the retry surface instead of the skeleton.
  const [startError, setStartError] = useState<string | null>(null);
  const [startRetryIn, setStartRetryIn] = useState(0);
  const [attempt, setAttempt] = useState<StoredAttempt | null>(null);
  const [secondsLeft, setSecondsLeft] = useState(0);
  // Escalated waiting line, shown only after ~15s of cumulative pending.
  const [waitingLong, setWaitingLong] = useState(false);
  // Terminal device-flow outcome kept inside the card ("Get a new code").
  const [deviceOutcome, setDeviceOutcome] = useState<DeviceOutcome | null>(null);
  const [copied, setCopied] = useState(false);
  // Single polite announcement channel for a11y (one-minute warning,
  // terminal outcomes). Countdown digits themselves stay aria-live="off".
  const [announce, setAnnounce] = useState<string | null>(null);

  const [pasteKey, setPasteKey] = useState("");
  const [pasteError, setPasteError] = useState<string | null>(null);
  const [pasteBanner, setPasteBanner] = useState<string | null>(null);
  const [pasteSubmitting, setPasteSubmitting] = useState(false);
  const [pasteRetryIn, setPasteRetryIn] = useState(0);

  const primaryButtonRef = useRef<HTMLButtonElement>(null);
  const codeRef = useRef<HTMLParagraphElement>(null);
  const pasteInputRef = useRef<HTMLInputElement>(null);
  // Poll-loop identity: incrementing cancels any in-flight loop at its next
  // checkpoint (every await re-checks the token).
  const pollRunRef = useRef(0);

  // ---- Countdown ---------------------------------------------------------
  useEffect(() => {
    if (phase !== "device" || attempt == null || deviceOutcome != null) return;
    setSecondsLeft(Math.max(0, Math.round((attempt.expires_at - Date.now()) / 1000)));
    const id = window.setInterval(() => {
      setSecondsLeft(Math.max(0, Math.round((attempt.expires_at - Date.now()) / 1000)));
    }, 1000);
    return () => window.clearInterval(id);
  }, [phase, attempt, deviceOutcome]);

  // Polite announcement when the countdown crosses into its last minute.
  // secondsLeft only decreases while active, so this fires exactly once.
  useEffect(() => {
    if (phase !== "device" || deviceOutcome != null) return;
    if (secondsLeft === 60) setAnnounce(LOGIN_COPY.device.oneMinuteLeft);
  }, [secondsLeft, phase, deviceOutcome]);

  // Expiry flips the card into its terminal state even if the poll loop is
  // between requests (e.g. a raised slow-down delay).
  useEffect(() => {
    if (phase !== "device" || deviceOutcome != null || attempt == null) return;
    if (secondsLeft > 0) return;
    pollRunRef.current += 1; // stop the poll loop
    clearAttempt();
    setWaitingLong(false);
    setDeviceOutcome("expired");
    setAnnounce(LOGIN_COPY.device.expired);
  }, [phase, deviceOutcome, attempt, secondsLeft]);

  // ---- Focus management ---------------------------------------------------
  // One effect owns focus per revealed surface: the user code when the
  // device card is live, the key field on paste, and the primary button
  // whenever the primary action is a button (start failure retry, terminal
  // "Get a new code"). This is the mount-focus requirement too: whichever
  // primary control appears first receives focus.
  useEffect(() => {
    if (phase === "device" && deviceOutcome == null) {
      codeRef.current?.focus();
    } else if (phase === "paste") {
      pasteInputRef.current?.focus();
    } else {
      primaryButtonRef.current?.focus();
    }
  }, [phase, deviceOutcome, startError]);

  // ---- Poll loop ----------------------------------------------------------
  const runPollLoop = useCallback(
    (stored: StoredAttempt) => {
      const run = ++pollRunRef.current;
      let pollDelayMs = stored.interval * 1000;
      let pendingMs = 0;

      async function loop(): Promise<void> {
        while (pollRunRef.current === run) {
          await delay(pollDelayMs + Math.random() * POLL_JITTER_MS);
          if (pollRunRef.current !== run) return;
          try {
            const res = await api.auth.pollDeviceLogin(stored.login_id);
            if (pollRunRef.current !== run) return;
            switch (res.status) {
              case "pending": {
                pendingMs += pollDelayMs;
                if (pendingMs >= WAITING_LONG_MS) setWaitingLong(true);
                continue;
              }
              case "authorized": {
                pollRunRef.current += 1;
                clearAttempt();
                setWaitingLong(false);
                setAnnounce(null);
                setPhase("authorizing");
                // Let the skeleton paint before handing off to the dashboard.
                window.setTimeout(() => router.replace(next), NAVIGATE_DELAY_MS);
                return;
              }
              case "expired": {
                pollRunRef.current += 1;
                clearAttempt();
                setWaitingLong(false);
                setDeviceOutcome("expired");
                setAnnounce(LOGIN_COPY.device.expired);
                return;
              }
              case "denied": {
                pollRunRef.current += 1;
                clearAttempt();
                setWaitingLong(false);
                setDeviceOutcome("denied");
                setAnnounce(LOGIN_COPY.device.denied);
                return;
              }
              default: {
                // "error": transient gateway-side failure — keep polling.
                continue;
              }
            }
          } catch (err: unknown) {
            if (pollRunRef.current !== run) return;
            if (err instanceof SlowDownError) {
              // Silent: raise the delay, keep waiting, never surface noise.
              pollDelayMs = Math.max(err.retrySeconds ?? SLOW_DOWN_FALLBACK_S, SLOW_DOWN_FALLBACK_S) * 1000;
              pendingMs += pollDelayMs;
              if (pendingMs >= WAITING_LONG_MS) setWaitingLong(true);
              continue;
            }
            const message = errorMessage(err);
            if (message.startsWith("404")) {
              // login_not_found: the attempt was reaped server-side.
              pollRunRef.current += 1;
              clearAttempt();
              setWaitingLong(false);
              setDeviceOutcome("lost");
              setAnnounce(LOGIN_COPY.device.attemptLost);
              return;
            }
            // 502/500: transient — stay quiet, retry on the same cadence.
            continue;
          }
        }
      }
      void loop();
    },
    [next],
  );

  // ---- Device start / restart ---------------------------------------------
  const startDeviceLogin = useCallback(async () => {
    pollRunRef.current += 1; // cancel any in-flight poll loop
    setPhase("starting");
    setStartError(null);
    setStartRetryIn(0);
    setDeviceOutcome(null);
    setWaitingLong(false);
    setCopied(false);
    setAnnounce(null);
    try {
      const start = await api.auth.startDeviceLogin();
      const stored = saveAttempt(start);
      setAttempt(stored);
      setSecondsLeft(start.expires_in);
      setPhase("device");
      runPollLoop(stored);
    } catch (err: unknown) {
      clearAttempt();
      setAttempt(null);
      if (errorMessage(err).startsWith("429")) {
        setStartRetryIn(RETRY_WINDOW_S);
        setStartError(LOGIN_COPY.startTooMany);
      } else {
        setStartError(LOGIN_COPY.startFailed);
      }
    }
  }, [runPollLoop]);

  // Mount: auto-start the device flow unless the visitor deep-linked into
  // the paste surface. A live stored attempt resumes polling instead of
  // minting a new code.
  useEffect(() => {
    if (deepLinkPaste) {
      setPhase("paste");
      return;
    }
    const stored = loadAttempt();
    if (stored != null) {
      setAttempt(stored);
      setSecondsLeft(Math.max(0, Math.round((stored.expires_at - Date.now()) / 1000)));
      setPhase("device");
      runPollLoop(stored);
    } else {
      void startDeviceLogin();
    }
    // Mount-only by design.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // ---- Paste 429 / start 429 countdowns ------------------------------------
  const pasteCountdownActive = pasteRetryIn > 0;
  useEffect(() => {
    if (!pasteCountdownActive) return;
    const id = window.setInterval(() => {
      setPasteRetryIn(prev => (prev <= 1 ? 0 : prev - 1));
    }, RETRY_TICK_MS);
    return () => window.clearInterval(id);
  }, [pasteCountdownActive]);

  const startCountdownActive = startRetryIn > 0;
  useEffect(() => {
    if (!startCountdownActive) return;
    const id = window.setInterval(() => {
      setStartRetryIn(prev => (prev <= 1 ? 0 : prev - 1));
    }, RETRY_TICK_MS);
    return () => window.clearInterval(id);
  }, [startCountdownActive]);

  // ---- Device screen actions ------------------------------------------------
  const handleCopy = useCallback(async () => {
    if (attempt == null) return;
    try {
      await navigator.clipboard.writeText(attempt.user_code);
      setCopied(true);
      window.setTimeout(() => setCopied(false), COPIED_RESET_MS);
    } catch {
      // Clipboard blocked (permissions/insecure context): the code stays
      // selectable text, so manual selection still works.
    }
  }, [attempt]);

  // Cancel → fresh start: drops the stored attempt and mints a new code.
  const handleCancel = useCallback(() => {
    clearAttempt();
    void startDeviceLogin();
  }, [startDeviceLogin]);

  // Switching back from paste resumes a still-live stored attempt; only a
  // genuinely absent/expired attempt mints a new code.
  const handleSwitchToDevice = useCallback(() => {
    setPasteError(null);
    setPasteBanner(null);
    const stored = loadAttempt();
    if (stored != null) {
      setAttempt(stored);
      setSecondsLeft(Math.max(0, Math.round((stored.expires_at - Date.now()) / 1000)));
      setDeviceOutcome(null);
      setPhase("device");
      runPollLoop(stored);
    } else {
      void startDeviceLogin();
    }
  }, [runPollLoop, startDeviceLogin]);

  // ---- Paste submit ----------------------------------------------------------
  async function handlePasteSubmit(e: React.FormEvent) {
    e.preventDefault();
    const key = pasteKey.trim();
    if (key === "" || pasteSubmitting || pasteRetryIn > 0) return;
    setPasteSubmitting(true);
    setPasteError(null);
    setPasteBanner(null);
    try {
      await api.auth.pasteKey(key);
      pollRunRef.current += 1;
      clearAttempt();
      setPhase("authorizing");
      window.setTimeout(() => router.replace(next), NAVIGATE_DELAY_MS);
    } catch (err: unknown) {
      const message = errorMessage(err);
      if (message.startsWith("401")) {
        // Inline field error; the pasted value is preserved and the field
        // is refocused for immediate correction.
        setPasteError(LOGIN_COPY.paste.invalidKey);
        pasteInputRef.current?.focus();
      } else if (message.startsWith("502")) {
        setPasteBanner(LOGIN_COPY.paste.gatewayDown);
      } else if (message.startsWith("429")) {
        setPasteRetryIn(RETRY_WINDOW_S);
      } else {
        setPasteBanner(LOGIN_COPY.paste.unexpected);
      }
    } finally {
      setPasteSubmitting(false);
    }
  }

  const pasteDisabled = pasteSubmitting || pasteRetryIn > 0;

  return (
    <div
      className="flex min-h-full w-full max-w-sm flex-col rounded-lg border border-border-darker bg-background p-6 shadow-sm sm:min-h-0"
      data-testid="login-card"
    >
      <h1 className="text-lg font-semibold text-foreground">{LOGIN_COPY.title}</h1>
      <p className="mt-1 text-sm text-muted-foreground">{LOGIN_COPY.subhead}</p>

      {sessionExpired && (
        <p className="mt-3 rounded-md bg-muted px-3 py-2 text-sm text-foreground" role="note">
          {LOGIN_COPY.sessionExpiredNote}
        </p>
      )}

      {phase === "starting" && startError == null && (
        <div className="mt-6 flex flex-col gap-3" aria-label={LOGIN_COPY.starting}>
          <p className="text-sm text-muted-foreground">{LOGIN_COPY.starting}</p>
          <Skeleton className="h-12 w-full" />
          <Skeleton.Text className="w-2/3" size="sm" />
          <Skeleton className="h-11 w-full" />
        </div>
      )}

      {phase === "starting" && startError != null && (
        <div className="mt-6 flex flex-col gap-3">
          <p role="alert" className="text-sm text-danger">
            {startError}
            {startRetryIn > 0 && ` ${startRetryIn}s`}
          </p>
          <Button
            ref={primaryButtonRef}
            intent={Intent.Default}
            appearance={Appearance.Filled}
            className="min-h-11"
            disabled={startRetryIn > 0}
            onClick={() => void startDeviceLogin()}
          >
            {LOGIN_COPY.retry}
          </Button>
          <button
            type="button"
            className="inline-flex min-h-11 items-center justify-center text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
            onClick={() => setPhase("paste")}
          >
            {LOGIN_COPY.paste.switchIn}
          </button>
        </div>
      )}

      {phase === "authorizing" && (
        <div className="mt-6 flex flex-col gap-3">
          <p className="text-sm text-foreground">{LOGIN_COPY.authorizing}</p>
          <Skeleton.Text className="w-full" size="lg" />
          <Skeleton.Text className="w-3/4" size="sm" />
          <Skeleton className="h-11 w-full" />
        </div>
      )}

      {phase === "device" && attempt != null && (
        <div className="mt-6 flex flex-col gap-4">
          {deviceOutcome == null ? (
            <>
              {/* The only aria-live="polite" line: waiting status, escalating
                  to "Still waiting…" after ~15s. */}
              <p aria-live="polite" className="text-sm text-muted-foreground">
                {waitingLong ? LOGIN_COPY.device.waitingLong : LOGIN_COPY.device.waiting}
              </p>

              <div>
                {/* Hidden second heading so keyboard users (and screen-reader
                    rotor navigation) can jump straight to the code section. */}
                <h2 id="login-user-code-label" className="sr-only">
                  {LOGIN_COPY.device.codeLabel}
                </h2>
                {/* Large, selectable, focusable code; focus lands here when
                    the code is revealed. select-all makes one click select
                    the whole code for manual copy. */}
                <p
                  ref={codeRef}
                  tabIndex={0}
                  aria-labelledby="login-user-code-label"
                  className="mt-2 cursor-text select-all rounded-md border border-border-darker bg-muted px-4 py-3 text-center font-mono text-[28px] leading-normal tracking-[0.25em] text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {attempt.user_code}
                </p>
                <div className="mt-2 flex items-center justify-between gap-2">
                  <Button appearance={Appearance.Outlined} intent={Intent.Default} className="min-h-11" onClick={() => void handleCopy()}>
                    {copied ? LOGIN_COPY.device.copied : LOGIN_COPY.device.copy}
                  </Button>
                  <p aria-live="off" className="text-xs text-muted-foreground">
                    {LOGIN_COPY.device.expiresPrefix} {formatCountdown(secondsLeft)}
                  </p>
                </div>
              </div>

              <Button
                appearance={Appearance.Filled}
                intent={Intent.Default}
                className="min-h-11"
                onClick={() => window.open(attempt.verification_uri_complete, "_blank", "noopener,noreferrer")}
              >
                {LOGIN_COPY.device.open}
              </Button>

              <p className="text-xs text-muted-foreground">
                {LOGIN_COPY.device.manualFallback}{" "}
                <a
                  href={attempt.verification_uri}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="underline underline-offset-4 hover:text-foreground"
                >
                  {LOGIN_COPY.device.manualFallbackCta}
                </a>
              </p>

              <button
                type="button"
                className="inline-flex min-h-11 items-center justify-center text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
                onClick={handleCancel}
              >
                {LOGIN_COPY.device.cancel}
              </button>
            </>
          ) : (
            <>
              <p role="alert" className="text-sm text-danger">
                {announce ?? LOGIN_COPY.device.expired}
              </p>
              <Button
                ref={primaryButtonRef}
                appearance={Appearance.Filled}
                intent={Intent.Default}
                className="min-h-11"
                onClick={() => void startDeviceLogin()}
              >
                {LOGIN_COPY.device.restart}
              </Button>
              <button
                type="button"
                className="inline-flex min-h-11 items-center justify-center text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
                onClick={() => setPhase("paste")}
              >
                {LOGIN_COPY.paste.switchIn}
              </button>
            </>
          )}
        </div>
      )}

      {phase === "paste" && (
        <div className="mt-6 flex flex-col gap-4">
          <p className="text-sm text-muted-foreground">{LOGIN_COPY.paste.intro}</p>

          {/* 502 / unexpected: banner above the field with a Retry action. */}
          {pasteBanner != null && (
            <div className="flex items-center justify-between gap-2 rounded-md bg-muted px-3 py-2">
              <p role="alert" className="text-sm text-foreground">
                {pasteBanner}
              </p>
              <Button appearance={Appearance.Outlined} intent={Intent.Default} size="sm" onClick={() => void pasteInputRef.current?.form?.requestSubmit()}>
                {LOGIN_COPY.retry}
              </Button>
            </div>
          )}

          {/* 429: message + disabled submit until the countdown elapses. */}
          {pasteRetryIn > 0 && (
            <p role="alert" className="text-sm text-danger">
              {LOGIN_COPY.paste.tooMany} {pasteRetryIn}s
            </p>
          )}

          <form onSubmit={e => void handlePasteSubmit(e)} className="flex flex-col gap-3" noValidate>
            <Input
              ref={pasteInputRef}
              label={LOGIN_COPY.paste.fieldLabel}
              placeholder={LOGIN_COPY.paste.placeholder}
              type="text"
              autoComplete="off"
              spellCheck={false}
              value={pasteKey}
              onChange={e => setPasteKey(e.target.value)}
              className="min-h-11"
            />
            {/* Inline field error; role="alert" so it announces. */}
            {pasteError != null && (
              <p role="alert" className="text-2xs text-danger">
                {pasteError}
              </p>
            )}
            <Button type="submit" appearance={Appearance.Filled} intent={Intent.Default} className="min-h-11" disabled={pasteDisabled}>
              {LOGIN_COPY.paste.submit}
            </Button>
          </form>

          <button
            type="button"
            className="inline-flex min-h-11 items-center justify-center text-sm text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
            onClick={handleSwitchToDevice}
          >
            {LOGIN_COPY.paste.switchBack}
          </button>
        </div>
      )}
    </div>
  );
}
