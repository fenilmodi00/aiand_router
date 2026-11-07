import { BarChart2, Cog, KeyRound, Plug, SlidersHorizontal } from "lucide-react";
import { type ReactNode } from "react";

export interface AppNavItem {
  href: string;
  label: string;
  icon: ReactNode;
}

/**
 * The top-level pages, in sidebar order. One label/icon config feeds both the
 * sidebar and each page's header (via PageShell), so the two cannot drift.
 */
export const APP_NAV: AppNavItem[] = [
  { href: "/dashboard", label: "Dashboard", icon: <BarChart2 size={16} /> },
  { href: "/keys", label: "API keys", icon: <KeyRound size={16} /> },
  { href: "/models", label: "Models & routing", icon: <SlidersHorizontal size={16} /> },
  { href: "/account", label: "Aiand account", icon: <Plug size={16} /> },
  { href: "/configuration", label: "Configuration", icon: <Cog size={16} /> },
];

export function appNavItem(href: string): AppNavItem {
  const item = APP_NAV.find(i => i.href === href);
  if (item == null) throw new Error(`unknown app route: ${href}`);
  return item;
}
