"use client";

import { PageShell } from "@/components/PageShell";
import { RouterKeysPanel } from "@/components/panels/RouterKeysPanel";

export default function KeysPage() {
  return (
    <PageShell href="/keys">
      <RouterKeysPanel />
    </PageShell>
  );
}
