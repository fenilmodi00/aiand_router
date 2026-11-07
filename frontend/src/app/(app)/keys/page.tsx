"use client";

import { PageShell } from "@/components/PageShell";
import { RouterKeysPanel } from "@/components/panels/RouterKeysPanel";

export default function KeysPage() {
  return (
    <PageShell href="/keys" description="Keys used to authenticate requests to this router.">
      <RouterKeysPanel />
    </PageShell>
  );
}
