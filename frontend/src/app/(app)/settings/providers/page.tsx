"use client";

import { AiandAccountPanel } from "@/components/settings/AiandAccountPanel";
import { SettingsPage, SettingsSection } from "@/components/settings/SettingsPage";
import { Plug } from "lucide-react";

export default function ProviderKeysSettingsPage() {
  return (
    <SettingsPage href="/settings/providers">
      <SettingsSection
        icon={<Plug className="size-4" />}
        title="Aiand account"
        description="Sign in to the Aiand account that supplies this router's upstream key."
      >
        <AiandAccountPanel />
      </SettingsSection>
    </SettingsPage>
  );
}
