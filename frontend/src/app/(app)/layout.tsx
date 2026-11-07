"use client";

import { useEffect, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import Link from "next/link";

import { Sidebar } from "@/components/Sidebar";
import { SidebarLayout } from "@/components/SidebarLayout";
import { api } from "@/lib/api";

type AuthState = "checking" | "authed" | "anonymous";

// Sidebar sign-out writes this key so sibling tabs (which don't share in-page
// state) bounce to /login via the storage listener below.
const LOGOUT_STORAGE_KEY = "aiand-router:logout";
const STATUS_POLL_MS = 30_000;

function DisconnectedBanner() {
  return (
    <div className="flex items-center justify-between gap-3 border-b border-danger/30 bg-danger/5 px-4 py-2 text-xs text-danger">
      <span>Aiand disconnected — requests are failing.</span>
      <Link
        href="/account"
        className="shrink-0 font-medium underline underline-offset-2 hover:opacity-80"
      >
        Reconnect
      </Link>
    </div>
  );
}

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const pathname = usePathname();
  const [state, setState] = useState<AuthState>("checking");
  // null until the first status probe lands; false renders the banner.
  const [aiandConnected, setAiandConnected] = useState<boolean | null>(null);

  useEffect(() => {
    let cancelled = false;
    api.auth
      .me()
      .then((res) => {
        if (cancelled) return;
        if (res.authenticated) {
          setState("authed");
        } else {
          setState("anonymous");
          const next = encodeURIComponent(pathname || "/dashboard");
          router.replace(`/login?next=${next}`);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setState("anonymous");
          router.replace("/login");
        }
      });
    return () => {
      cancelled = true;
    };
  }, [pathname, router]);

  // Upstream-credential state drives the disconnected banner: polled so a
  // credential expiring in another tab still surfaces, and re-probed on
  // navigation (returning from the login reconnect flow).
  useEffect(() => {
    if (state !== "authed") return;
    let cancelled = false;
    function refresh() {
      api.aiandAuth
        .status()
        .then((s) => {
          if (!cancelled) setAiandConnected(s.connected);
        })
        // Non-fatal: a failed probe just keeps the previous banner state.
        .catch(() => undefined);
    }
    refresh();
    const interval = window.setInterval(refresh, STATUS_POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [state, pathname]);

  // Signed out in another tab → follow it to /login.
  useEffect(() => {
    function onStorage(e: StorageEvent) {
      if (e.key === LOGOUT_STORAGE_KEY && e.newValue != null) {
        router.replace("/login");
      }
    }
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, [router]);

  if (state !== "authed") {
    return <SidebarLayout sidebar={<div className="md:w-[244px]" />}>{null}</SidebarLayout>;
  }

  return (
    <SidebarLayout sidebar={<Sidebar />}>
      {aiandConnected === false ? (
        <div className="flex h-full flex-col">
          <DisconnectedBanner />
          <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
        </div>
      ) : (
        children
      )}
    </SidebarLayout>
  );
}
