"use client";

import { SWRConfig } from "swr";
import type { ReactNode } from "react";

/**
 * Global SWR cache config: navigating between dashboard pages renders
 * instantly from the shared cache and revalidates in the background
 * (revalidate-on-focus picks up changes made in sibling tabs).
 */
export function SWRProvider({ children }: { children: ReactNode }) {
  return (
    <SWRConfig value={{ revalidateOnFocus: true, dedupingInterval: 2000 }}>
      {children}
    </SWRConfig>
  );
}
