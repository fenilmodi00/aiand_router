"use client";

import { appNavItem } from "@/components/nav";
import { Text } from "@/components/atoms/Text";
import { Page } from "@/components/Page";
import { PageHeader } from "@/components/PageHeader";
import { type ReactNode } from "react";

export interface PageShellProps {
  href: string;
  children: ReactNode;
}

/** Shell for a top-level page: header sourced from the shared nav config. */
export function PageShell({ href, children }: PageShellProps) {
  const item = appNavItem(href);
  return (
    <Page
      header={
        <PageHeader
          left={
            <Text
              variant="h4"
              as="h2"
              className="flex flex-row items-center gap-2 whitespace-nowrap"
            >
              {item.icon}
              {item.label}
            </Text>
          }
        />
      }
    >
      <div className="flex w-full max-w-text-width flex-col gap-2">
        {children}
      </div>
    </Page>
  );
}

export interface PageShellSectionProps {
  icon: ReactNode;
  title: string;
  description: ReactNode;
  children: ReactNode;
}

export function PageShellSection({
  children,
  description,
  icon,
  title,
}: PageShellSectionProps) {
  return (
    <Page.Section
      className="py-3"
      header={
        <Page.SectionHeader>
          {icon}
          <Text variant="h4" as="h3">
            {title}
          </Text>
        </Page.SectionHeader>
      }
    >
      <Text className="text-xs text-muted-foreground">{description}</Text>
      {children}
    </Page.Section>
  );
}

PageShell.Section = PageShellSection;
