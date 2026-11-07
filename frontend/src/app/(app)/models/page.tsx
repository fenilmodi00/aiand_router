"use client";

import { Filter, SlidersHorizontal } from "lucide-react";

import { PageShell } from "@/components/PageShell";
import { ModelSelectionPanel } from "@/components/panels/ModelSelectionPanel";
import { RoutingPriorityPanel } from "@/components/panels/RoutingPriorityPanel";

export default function ModelsPage() {
  return (
    <PageShell href="/models">
      <PageShell.Section
        icon={<SlidersHorizontal className="size-4" />}
        title="Routing priority"
        description="Bias routing toward stronger models or cheaper ones. Every request is balanced between the two. Leave as default to let the router decide."
      >
        <RoutingPriorityPanel />
      </PageShell.Section>
      <PageShell.Section
        icon={<Filter className="size-4" />}
        title="Model selection"
        description="Uncheck a model to drop it from routing decisions for this installation. Unchecked models are skipped at request time."
      >
        <ModelSelectionPanel />
      </PageShell.Section>
    </PageShell>
  );
}
