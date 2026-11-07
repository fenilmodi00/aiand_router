"use client";

import { PageShell } from "@/components/PageShell";
import { ConfigPanel } from "@/components/panels/ConfigPanel";

export default function ConfigurationPage() {
  return (
    <PageShell href="/configuration" description="Runtime values set via environment variables.">
      <ConfigPanel />
    </PageShell>
  );
}
