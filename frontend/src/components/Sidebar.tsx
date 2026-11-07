"use client";

import { Logo } from "./Logo";
import { APP_NAV, type AppNavItem } from "./nav";
import { Button } from "@/components/molecules/Button";
import { Tooltip } from "@/components/molecules/Tooltip";
import { Appearance } from "@/components/types";
import { api } from "@/lib/api";
import { cn } from "@/lib/cn";
import { LogOut } from "lucide-react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";

// Matches the storage listener in (app)/layout.tsx: writing a timestamp makes
// every sibling tab bounce to /login.
const LOGOUT_STORAGE_KEY = "aiand-router:logout";

function NavLink({ item }: { item: AppNavItem }) {
  const pathname = usePathname();
  const active = pathname === item.href || pathname.startsWith(item.href + "/");

  return (
    <Link
      href={item.href}
      aria-selected={active}
      title={item.label}
      className={cn(
        "relative flex h-8 w-full items-center gap-2 rounded-md px-3 text-xs font-medium text-muted-foreground transition-colors",
        "hover:bg-foreground/5 hover:text-foreground",
        "aria-selected:bg-foreground/5 aria-selected:text-foreground",
      )}
    >
      <span className="shrink-0">{item.icon}</span>
      <span className="hidden whitespace-nowrap md:inline">{item.label}</span>
    </Link>
  );
}

export function Sidebar() {
  const router = useRouter();

  async function handleSignOut() {
    try {
      await api.auth.logout();
    } catch {
      // Best-effort: even if the network call fails, redirect to /login so
      // the user is no longer in a half-authed state.
    }
    // Sibling tabs watch this key via the storage event in (app)/layout.tsx
    // and follow to /login.
    try {
      window.localStorage.setItem(LOGOUT_STORAGE_KEY, String(Date.now()));
    } catch {
      // Non-fatal: this tab still redirects; other tabs recover on their own
      // next 401.
    }
    router.replace("/login");
  }

  return (
    <div className="group/sidebar relative flex h-full w-12 shrink-0 grow-0 flex-col items-start gap-1 overflow-hidden transition-all duration-200 ease-out md:w-[244px] md:overflow-visible">
      <header className="relative z-10 flex w-full flex-col items-center gap-4 py-2 transition-all duration-200 md:flex-row md:pl-2 md:pr-3 md:pt-2">
        <Logo href="/dashboard" />
      </header>

      <nav className="relative z-10 flex w-full flex-1 flex-col gap-1 overflow-y-auto md:p-2 md:pt-0">
        {APP_NAV.map(item => (
          <NavLink key={item.href} item={item} />
        ))}
      </nav>

      <div className="relative z-10 flex w-full flex-col items-stretch gap-2 p-2">
        <Tooltip content="Sign out" side="left" interactiveChild>
          <Button
            appearance={Appearance.Hollow}
            aria-label="Sign out"
            className={sidebarFooterButton}
            onClick={() => {
              void handleSignOut();
            }}
          >
            <LogOut className="size-4 shrink-0" />
            <span className="hidden whitespace-nowrap text-xs md:inline">Sign out</span>
          </Button>
        </Tooltip>
      </div>
    </div>
  );
}

// Fixed size while collapsed; on md+ the sidebar is wide enough for the
// labelled row, so width and alignment open up.
const sidebarFooterButton =
  "size-8 shrink-0 justify-center rounded-md border border-border-darker bg-muted p-0 text-muted-foreground hover:bg-border-darker hover:text-foreground md:h-8 md:w-full md:justify-start md:px-3";
