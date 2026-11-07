"use client";

import { AiandAccountPanel } from "@/components/panels/AiandAccountPanel";
import { PageShell } from "@/components/PageShell";
import { api } from "@/lib/api";
import { useEffect, useState } from "react";

function SignedInAs() {
  const [email, setEmail] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    api.auth
      .me()
      .then((res) => {
        if (!cancelled && res.authenticated && res.email != null) setEmail(res.email);
      })
      // Non-fatal: the row simply doesn't render when the probe fails.
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, []);

  if (email == null) return null;

  return (
    <p className="text-2xs text-muted-foreground">
      Signed in as <span className="font-medium text-foreground">{email}</span>
    </p>
  );
}

export default function AccountPage() {
  return (
    <PageShell
      href="/account"
      description="Sign in to the Aiand account that supplies this router's upstream key."
    >
      <SignedInAs />
      <AiandAccountPanel />
    </PageShell>
  );
}
