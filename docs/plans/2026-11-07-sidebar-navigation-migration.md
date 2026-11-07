# Sidebar Navigation Migration — settings pages become top-level pages

Status: **implemented** on `feat/sidebar-navigation` (PR #127).

Goal: every panel currently reachable through `/settings` becomes a first-class
sidebar page, and the settings shell (`SettingsSidebar`, `SETTINGS_NAV`,
`SettingsPage`, `/settings/*`) is removed entirely.

## Current state review

### Routes and shell

| Current route | Page file | Content |
|---|---|---|
| `/dashboard` | `frontend/src/app/(app)/dashboard/page.tsx` | Metrics, charts, onboarding, disconnected CTA |
| `/settings` | `frontend/src/app/(app)/settings/page.tsx` | `SignedInAs`, `RouterKeysPanel`, `ConfigPanel` |
| `/settings/providers` | `frontend/src/app/(app)/settings/providers/page.tsx` | `AiandAccountPanel` |
| `/settings/models` | `frontend/src/app/(app)/settings/models/page.tsx` | `RoutingPriorityPanel`, `ModelSelectionPanel` |

The authed shell swaps sidebars on a pathname prefix:

```tsx
// frontend/src/app/(app)/layout.tsx
const sidebar = pathname.startsWith("/settings") ? <SettingsSidebar /> : <Sidebar />;
```

`SettingsSidebar` duplicates the main sidebar chrome (logo, width, nav column)
and renders `SETTINGS_NAV` from `frontend/src/components/settings/nav.tsx`.
`SettingsPage` looks its title/icon up from the same config, so sidebar label and
page title are coupled through `/settings` hrefs.

### Findings that shape the migration

1. **Two sidebars, one duplicated shell.** `SettingsSidebar.tsx` is a copy of
   `Sidebar.tsx` with a back button and a different nav list. Removing it also
   removes the pathname switch and the `/settings` index special-case in active
   matching.
2. **Three settings routes are really four panels.** The General page packs two
   unrelated surfaces: router key management and read-only runtime configuration.
   Promoting "General" as a top-level label would be ambiguous, so split it into
   **API keys** and **Configuration**.
3. **Nav labels and page titles share one config today.** Keep that property
   with a new `APP_NAV` config; otherwise five pages and the sidebar drift.
4. **`NavItem.matchPrefix` is dead.** No `PRIMARY_NAV` entry uses it. Remove it
   while moving the nav list.
5. **Entry links point at `/settings/providers`.** Both the persistent
   disconnected banner (`(app)/layout.tsx`) and the dashboard disconnected CTA
   (`(app)/dashboard/page.tsx`) must move to the new account route.
6. **Reconnect CTAs lose their destination.** `AiandAccountPanel` sends users to
   `/login` and `/login?mode=paste`; after login they land on `/dashboard`, not
   back on the account page. Point both at `next=/account` so the new page is the
   return surface. The `?connect=1` branch that opens the disconnect confirm on
   mount has no producer; delete it.
7. **Settings is static-exported.** `next.config.ts` uses `output: "export"`,
   `basePath: "/ui"`, `trailingSlash: false`; the Go server maps `/ui/<path>` to
   `<path>.html` in `registerUIStatic`. Next `redirects()` is unavailable in the
   export path, so production legacy redirects belong in the Go static handler.
   Dev needs equivalent redirects for parity.
8. **Docs contain user-facing navigation instructions.** README,
   `docs/ANALYTICS_EXPORT.md`, `docs/CONFIGURATION.md`, and three install
   references say "Settings → …" or "dashboard's settings page". They must name
   the new pages. References to a client's own settings UI or the hosted Aiand
   app are out of scope.
9. **No frontend test runner.** CI gates are `npm run typegen` and
   `npm run typecheck`; the meaningful artifact check is a successful static
   export with the new HTML routes present and the settings HTML routes absent.
10. **The baseline includes unrelated cleanup** (commit `00f6b6e3`: theme/CSS
    pruning, removed `info` accent, removed layout auth event, removed
    root-page `safeNext`). Do not revert or fold it into this migration.
11. **Baseline typecheck is green** (`npm run typegen && npm run typecheck`).
    Navigation work must not touch `frontend/src/components/Chart/**`.

## Target navigation

| Nav item | Route | Content | Legacy route |
|---|---|---|---|
| Dashboard | `/dashboard` | Existing dashboard | unchanged |
| API keys | `/keys` | `RouterKeysPanel` | `/settings` |
| Models & routing | `/models` | `RoutingPriorityPanel`, `ModelSelectionPanel` | `/settings/models` |
| Aiand account | `/account` | `SignedInAs`, `AiandAccountPanel` | `/settings/providers` |
| Configuration | `/configuration` | `ConfigPanel` | `/settings` (secondary section) |

- Sidebar order: Dashboard, API keys, Models & routing, Aiand account,
  Configuration.
- The Settings gear in the sidebar footer is removed; Sign out stays.
- `/settings` redirects to `/keys`; `/settings/providers` to `/account`;
  `/settings/models` to `/models`. Queries are preserved.
- Icons: keep `BarChart2`, `KeyRound`, `SlidersHorizontal`, `Plug`; use `Cog`
  for Configuration.
- `/account` does not collide with the API base `/account/v1`: API calls are
  root-absolute, while the page lives under the `/ui` basePath. The Next dev
  rewrite targets `/account/:path*` at the root, not `/ui/account`.

### Page hierarchy

Dashboard keeps its current "Router cost & savings" header and layout.

The four new pages use one shell so header/body width cannot drift:

```tsx
// @/components/PageShell
<PageShell href="/keys" description="Keys used to authenticate requests to this router.">
  <RouterKeysPanel />
</PageShell>
```

`PageShell` resolves label/icon from `appNavItem(href)` and renders the existing
`Page` + `PageHeader` + `Text variant="h4"` pattern with a
`max-w-text-width` body. `PageShell.Section` is the current `SettingsSection`
(icon, title, description) and is used for the two sections on `/models`.

Section wrappers are dropped where the panel already owns a card title:
`/keys`, `/account`, and `/configuration` render a short page description and
the panel. `/models` keeps both labeled sections because those panels have no
internal headings.

## File-level migration

### Create

| Path | Contents |
|---|---|
| `frontend/src/components/nav.tsx` | `AppNavItem`, `APP_NAV` (all five items), `appNavItem(href)` |
| `frontend/src/components/PageShell/PageShell.tsx` | `PageShell`, `PageShell.Section` |
| `frontend/src/components/PageShell/index.ts` | `export * from "./PageShell";` |
| `frontend/src/components/panels/RouterKeysPanel.tsx` | Moved, unchanged |
| `frontend/src/components/panels/AiandAccountPanel.tsx` | Moved; link/comment cleanup |
| `frontend/src/components/panels/ConfigPanel.tsx` | Moved, unchanged |
| `frontend/src/components/panels/ModelSelectionPanel.tsx` | Moved, unchanged |
| `frontend/src/components/panels/RoutingPriorityPanel.tsx` | Moved, unchanged |
| `frontend/src/components/panels/shared.tsx` | Moved, unchanged |
| `frontend/src/app/(app)/keys/page.tsx` | API keys page |
| `frontend/src/app/(app)/models/page.tsx` | Models & routing page |
| `frontend/src/app/(app)/account/page.tsx` | Aiand account page with `SignedInAs` |
| `frontend/src/app/(app)/configuration/page.tsx` | Configuration page |
| `internal/server/ui_static_test.go` | Legacy redirect + clean-URL tests (`package server`) |

### Modify

| Path | Change |
|---|---|
| `frontend/src/components/Sidebar.tsx` | Render `APP_NAV`; delete local `NavItem`/`PRIMARY_NAV`/`matchPrefix`; remove Settings footer button and `Settings` icon import; update footer-button comment |
| `frontend/src/app/(app)/layout.tsx` | Remove `SettingsSidebar` import and pathname switch; always `<Sidebar />`; banner link → `/account` |
| `frontend/src/app/(app)/dashboard/page.tsx` | Disconnected CTA link → `/account` |
| `frontend/src/app/(auth)/login/page.tsx` | Update `safeNext` comment example from `/settings?tab=keys` to `/keys`; map legacy `next` paths |
| `frontend/next.config.ts` | Dev-only legacy redirects for the three old paths |
| `internal/server/server.go` | Legacy redirect map in `registerUIStatic`; update `/ui/settings` examples in comments |
| `README.md` | "dashboard's settings page" → "dashboard's **Models & routing** page" |
| `docs/ANALYTICS_EXPORT.md` | Self-hosted key instructions → **API keys** page |
| `docs/CONFIGURATION.md` | Router dashboard revoke instruction → **API keys** page |
| `install/README.md` | "router dashboard's settings page" → "**Models & routing** page" |
| `install/commands/models.md` | Same wording |
| `install/commands/router-models.md` | Same wording |
| `install/codex-skills/router-models/SKILL.md` | Same wording |

Do **not** edit: Cursor settings instructions in `README.md`, `install/README.md`,
or `install/npm/README.md`; hosted-dashboard "Settings → Provider API keys"
references in `docs/CONFIGURATION.md`; historical plans under `docs/plans/`.

### Move

`frontend/src/components/settings/{RouterKeysPanel,AiandAccountPanel,ConfigPanel,ModelSelectionPanel,RoutingPriorityPanel}.tsx`
and `shared.tsx` → `frontend/src/components/panels/`.

### Delete

| Path | Reason |
|---|---|
| `frontend/src/components/SettingsSidebar.tsx` | Settings shell removed |
| `frontend/src/components/settings/nav.tsx` | Replaced by `components/nav.tsx` |
| `frontend/src/components/settings/SettingsPage.tsx` | Replaced by `PageShell` |
| `frontend/src/app/(app)/settings/page.tsx` | Replaced by `/keys` + `/configuration` |
| `frontend/src/app/(app)/settings/providers/page.tsx` | Replaced by `/account` |
| `frontend/src/app/(app)/settings/models/page.tsx` | Replaced by `/models` |
| Empty `frontend/src/components/settings/` and `frontend/src/app/(app)/settings/` dirs | No remaining files |

`SettingsSection` must not survive as an alias; its implementation becomes
`PageShell.Section`.

## Legacy URL handling

Production (`internal/server/server.go`), after normalizing the `/ui/` prefix and
trailing slash, before file resolution:

```go
var legacyUIPathRedirects = map[string]string{
	"settings":           "/ui/keys",
	"settings/providers": "/ui/account",
	"settings/models":    "/ui/models",
}
```

- 308 Permanent Redirect, matching the existing trailing-slash redirect.
- Append `?` + `RawQuery` when present so `/ui/settings?tab=keys` keeps its query.
- `/ui/settings/` may take two hops (slash normalize, then legacy map); acceptable
  and covered by a test.
- Dev (`next.config.ts`): add the same three redirects inside the existing
  dev-only `redirects()` block, then verify the effective source form
  (`/settings` auto-prefixed by basePath vs `/ui/settings` with `basePath: false`).

Stale post-login destinations (a tab open across deploy, a stored device attempt)
are normalized in `login/page.tsx`'s `safeNext`:

```ts
const LEGACY_NEXT: Record<string, string> = {
  "/settings": "/keys",
  "/settings/providers": "/account",
  "/settings/models": "/models",
};
```

Apply after the same-origin checks, preserving the query string. The root browser
callback uses the backend-returned `next`, so add the same path map to
`internal/auth/browser_login.go`'s `sanitizeBrowserNext` (exact-path lookup, query
preserved) and extend `TestStartBrowserLogin_SanitizesNext`. This is the same
three-entry migration table in three places; keep comments pointing at this plan.

## Behavior and edge cases

- `usePathname()` excludes the `/ui` basePath today (the `/settings` check works),
  so active matching stays `/dashboard`, `/keys`, etc.
- No route pairs can prefix-match each other, so exact match is enough for all
  five items; remove the dead `matchPrefix` helper.
- Collapsed sidebar (`w-12`) still shows icons with `title`/`aria-label`; labels
  appear at `md` because `APP_NAV` feeds the existing `hidden md:inline` span.
- Sign out, its storage-key cross-tab logout, the auth gate, the disconnected
  banner, and the status poll are unchanged.
- Panels keep all `api.*` calls and mutations; only import paths change.
- Dropping the settings gear is a navigation change only; no route or capability
  is lost because every panel now has a top-level item.

## Implementation phases (for subagent dispatch)

All wave-1 tasks are file-disjoint and share the contracts below.

**Shared contracts**

- `@/components/nav` exports `APP_NAV: AppNavItem[]` and
  `appNavItem(href: string): AppNavItem`; throw on unknown href.
- `@/components/PageShell` exports `PageShell({ href, description?, children })`
  and `PageShell.Section({ icon, title, description, children })`.
- Panels live at `@/components/panels/<Name>`; `shared.tsx` stays beside them.
- Route → content mapping is fixed by the target table above.

### Wave 1 (parallel)

| Task | Files owned | Work |
|---|---|---|
| T1 Chrome | `components/nav.tsx`, `components/PageShell/*`, `components/Sidebar.tsx`, `(app)/layout.tsx`, `SettingsSidebar.tsx` (delete), login comment/legacy map, `next.config.ts` | New nav config + shell; simplify sidebar and layout; dev redirects |
| T2 Panel move + account page | All `components/settings/*.tsx` panels + `shared.tsx` → `components/panels/`; delete `settings/nav.tsx`, `SettingsPage.tsx`, old `(app)/settings/**`; create `(app)/account/page.tsx` (owns the `SignedInAs` source it deletes); account panel link/comment cleanup | `git mv`, import updates, dead `?connect=1` removal, reconnect `next=/account` |
| T3 Other routes | Three new `(app)/{keys,models,configuration}/page.tsx` | Compose `PageShell` + panels using the fixed mapping; no dependency on deleted settings files |
| T4 Server | `internal/server/server.go`, `internal/server/ui_static_test.go`, `internal/auth/browser_login.go`, `internal/auth/login_service_test.go` | Legacy redirects, static-handler tests, browser-next normalization + test |
| T5 Docs | README, ANALYTICS_EXPORT, CONFIGURATION, install README/commands/skill | Copy updates only |

T1 and T2 both touch the old settings area only through deletion; ownership is
split so no file is edited by two tasks. T3 writes new files only. T4 and T5 are
disjoint from frontend source.

### Wave 2 (orchestrator)

1. Re-read every changed file after subagents finish (concurrent tree is dirty).
2. Run `cd frontend && npm run typegen && npm run typecheck && npm run build`.
3. Assert export map: `out/keys.html`, `out/models.html`, `out/account.html`,
   `out/configuration.html` exist; `out/settings*.html` and `out/settings/` do not.
4. Run `go test ./internal/server/... ./internal/auth/...` and `make check-docs`.
5. Browser smoke: run the export (or `next dev`) against a throwaway mock API
   returning authenticated `me`, connected `aiand-auth` status, and minimal panel
   payloads; click all five nav items, verify exactly one active item, page
   titles, and no `/settings` link remains. Remove the mock afterward.
6. No formatter is configured for the frontend; do not introduce one. Match
   surrounding style.

## Acceptance criteria

- The sidebar shows exactly: Dashboard, API keys, Models & routing, Aiand account,
  Configuration; no Settings gear and no settings sub-sidebar.
- Each item opens its page and highlights only itself; direct URL load works under
  `/ui/<route>` via the Go clean-URL handler.
- Every capability from the old settings pages is reachable: key issue/rotate/
  revoke/search, Aiand connect/disconnect/reconnect, routing priority, model
  inclusion, runtime config, signed-in email.
- `/settings`, `/settings/providers`, and `/settings/models` redirect to
  `/keys`, `/account`, and `/models` (query preserved) in production and dev.
- Disconnected banner and dashboard CTA land on `/account`, which offers both
  reconnect paths and returns there after login.
- No symbol named `SettingsSidebar`, `SETTINGS_NAV`, `SettingsPage`, or
  `SettingsSection` remains; `components/settings/` is gone.
- `npm run typegen && npm run typecheck && npm run build` is green; Go tests and
  `make check-docs` are green.
- No backend API, database, billing, or routing behavior changes.

## Rollback

Single static-export deploy plus one Go static-handler change. Revert the commit
and redeploy; no data migration or persisted nav state exists. Old URLs continue
to work in the reverted build.
