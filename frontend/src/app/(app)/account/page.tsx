"use client";

import { AiandAccountPanel } from "@/components/panels/AiandAccountPanel";
import { PageShell } from "@/components/PageShell";

export default function AccountPage() {
  return (
    <PageShell href="/account">
      <AiandAccountPanel />
    </PageShell>
  );
}
