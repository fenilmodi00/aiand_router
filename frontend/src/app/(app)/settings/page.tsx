"use client";

import { ConfigPanel } from "@/components/settings/ConfigPanel";
import { RouterKeysPanel } from "@/components/settings/RouterKeysPanel";
import { SettingsPage, SettingsSection } from "@/components/settings/SettingsPage";
import { api } from "@/lib/api";
import { KeyRound, Settings as SettingsIcon } from "lucide-react";
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

export default function GeneralSettingsPage() {
  return (
    <SettingsPage href="/settings">
      <SignedInAs />
      <SettingsSection
        icon={<KeyRound className="size-4" />}
        title="Router API keys"
        description="Keys used to authenticate requests to this router."
      >
        <RouterKeysPanel />
      </SettingsSection>

      <SettingsSection
        icon={<SettingsIcon className="size-4" />}
        title="Configuration"
        description="Runtime values set via environment variables."
      >
        <ConfigPanel />
      </SettingsSection>
    </SettingsPage>
  );
}
