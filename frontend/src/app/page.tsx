"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";

import { Button } from "@/components/molecules/Button";
import { Appearance, Intent } from "@/components/types";
import { api } from "@/lib/api";

// Bare-root callback for the browser (authorization-code) sign-in. The
// provider redirects to the frontend origin with ?code&state (or ?error);
// Next serves this page at /ui and the root 307 preserves the query here.
// Login redirects go to /ui/login?mode=device on any failure.

const CALLBACK_COPY = {
  exchanging: "Finishing sign-in…",
  failedTitle: "Sign-in couldn't be completed.",
  providerRejected: "The sign-in was rejected before it reached the router.",
  retryDevice: "Sign in with a device code",
} as const;

function CallbackInner() {
  const router = useRouter();
  const params = useSearchParams();
  const code = params.get("code");
  const state = params.get("state");
  // Provider-side failure (access_denied, invalid_request, …) or a malformed
  // callback: skip the exchange entirely and render the failure card.
  const providerError = params.get("error");
  const [failed, setFailed] = useState(providerError != null);

  // React StrictMode mounts effects twice in development; without this guard
  // the second mount would replay the exchange after the cookie was already
  // consumed, turning the retry into a guaranteed invalid_state 400.
  const exchangedRef = useRef(false);

  useEffect(() => {
    if (providerError != null || code == null || state == null) {
      if (providerError == null && code == null && state == null) {
        // Bare visit to /ui: straight to the dashboard, which bounces to
        // login when there is no session.
        router.replace("/dashboard");
      }
      return;
    }
    if (exchangedRef.current) return;
    exchangedRef.current = true;
    let cancelled = false;
    (async () => {
      try {
        const res = await api.auth.exchangeBrowserCode(code, state);
        if (cancelled) return;
        // Warm the target route while the session cookie lands, then hand off.
        router.prefetch(res.next);
        router.replace(res.next);
      } catch {
        if (!cancelled) setFailed(true);
      }
    })();
    return () => {
      cancelled = true;
    };
    // Mount-only: the query string is fixed for this page load.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (failed) {
    return (
      <div className="flex min-h-full w-full max-w-sm flex-col rounded-lg border border-border-darker bg-background p-6 shadow-sm sm:min-h-0">
        <h1 className="text-lg font-semibold text-foreground">{CALLBACK_COPY.failedTitle}</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          {providerError != null ? CALLBACK_COPY.providerRejected : CALLBACK_COPY.failedTitle}
        </p>
        <Button
          appearance={Appearance.Filled}
          intent={Intent.Default}
          className="mt-6 min-h-11"
          onClick={() => router.replace("/login?mode=device")}
        >
          {CALLBACK_COPY.retryDevice}
        </Button>
      </div>
    );
  }

  return (
    <div className="flex min-h-full w-full max-w-sm flex-col rounded-lg border border-border-darker bg-background p-6 shadow-sm sm:min-h-0">
      <p className="text-sm text-muted-foreground">{CALLBACK_COPY.exchanging}</p>
    </div>
  );
}

export default function BrowserCallbackPage() {
  return (
    <Suspense fallback={null}>
      <CallbackInner />
    </Suspense>
  );
}
