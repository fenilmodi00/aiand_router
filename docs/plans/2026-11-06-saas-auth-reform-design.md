# Dashboard Auth Reform — SaaS Self-Serve Login

Design doc for replacing `ROUTER_ADMIN_PASSWORD` dashboard auth with Aiand-account login (device flow or pasted Aiand key), SaaS-style: every user gets their own isolated installation, keys, credentials, and metrics. Includes the diagnosed re-auth persistence bug.

Status: **draft for approval** — nothing below is implemented yet.

## Intent and why

The dashboard is being repositioned from a single-operator password site to a self-serve product: each person signs in with **their own Aiand account** (OAuth2 device flow in the browser, or by pasting their Aiand `sk-` key) and lands in an isolated tenant — own installation, own routing keys, own upstream `aiand` credential, own metrics. No admin role, no shared password, no shared credential. First-ever visitor must be able to bootstrap with zero configuration.

A manual test found a blocking bug: after device auth → disconnect, re-auth fails and no credential is saved. Diagnosis (live Docker logs, 4/4 reproducible): paste logins write a nil refresh ciphertext into `aiand_credentials.refresh_token_ciphertext BYTEA NOT NULL` → SQLSTATE 23502 → HTTP 500, and nothing persists. Details in "Bug" section.

## Goals

1. Remove `ROUTER_ADMIN_PASSWORD` and the whole admin-password axis (login, cookie, limiter, singleton fallback).
2. Login = Aiand identity: device flow OR pasted Aiand key. Either path authenticates and creates/uses the user's own installation.
3. First interaction bootstraps: fresh deployment, no DB rows → first device/paste login creates account + installation + upstream credential + session, unauthenticated.
4. Per-user isolation: no user can see or mutate another user's installation, keys, credentials, or metrics.
5. Fix the paste/re-auth persistence bug and its latent siblings (slow-down contract drift, pending-lifecycle flaws).

## Non-goals

- No per-user routing inside one installation (Aiand org tenancy with credential subjects — recorded as Approach B and rejected for now).
- No router `rk_`/`ra_` key as a dashboard login credential. It reverses a documented boundary (`WithAdminOnly` exists to keep data-plane keys off management routes).
- No change to managed mode (`/v1/*`, billing, control plane). SaaS login is a selfhosted-block surface.
- No multi-site/federation concerns; one deployment = one database.

## Constraints (from repo rules + scout findings)

- `internal/auth` is inner-ring I/O-free. Note: it currently imports `internal/aiandauth` (HTTP client) — an existing violation. New login code must define the gateway interface + DTOs in `auth` and keep HTTP in `internal/aiandauth` (adapter), wired in `cmd/router/main.go`. No new HTTP or SQL in `auth`.
- SQLC only: all new SQL in `db/queries/`, regenerate `internal/sqlc`, commit generated code. Migrations start at `0143`, `BEGIN/COMMIT`, exact down, no `IF EXISTS`.
- `installations.external_id` is the Tink AAD for `aiand_credentials` ciphertexts and the billing org key. **Never rename existing rows** (AAD makes stored ciphertexts undecryptable). New accounts get fresh opaque IDs.
- No FKs to external ids; `aiand_user_id`/`aiand_org_id` are opaque `VARCHAR`s.
- Named constants for provider names, cookie names, modes. Tests: no DB-backed tests in `internal/` (use `scripts/` + `ROUTER_TEST_DATABASE_URL` pattern, e.g. `scripts/upsert_external_key_race_check`).
- Route registration: new login endpoints inside the selfhosted block of `server.Register`.
- Frontend is Next.js static export under `/ui`; auth is ambient cookies (`credentials: "include"`), client-side gate in `(app)/layout.tsx`.

## Approaches considered

### A. One installation per Aiand account (recommended)

Router-owned `accounts` table keyed on `(aiand_user_id, aiand_org_id)` → 1:1 `installation_id`. Login creates account + installation on first success; every existing per-installation mechanism (`aiand_credentials` UNIQUE(installation_id), external-key uniqueness, key scoping, metrics filters, rotation sweep) works unchanged. Session = opaque cookie token → hashed row in `account_sessions`. Prior art: `origin/feat/aiand-self-service-login` (f5357c9) built accounts + sessions for key-login; we add device flow, revocation, and user-keyed identity.

- Pros: minimal schema delta (2 new tables + 1 login-attempts table); no AAD change (fresh opaque account IDs); isolation is native (installation-scoped everything); smallest migration risk; legacy `__router_admin__` row left dormant.
- Cons: per-user billing identity (each installation bills separately — for a BYOK product this is fine); duplicate installations if one human is in two gateway orgs (acceptable, matches "own account, own data").

### B. Installation per Aiand org + per-user credential subjects

Installation = org tenant; each dashboard user is a `credential_subjects` row; credentials/keys/metrics become subject-scoped; AAD must include subject → cross-system re-seal of every stored ciphertext.

- Pros: org-level billing/spend aggregation.
- Cons: largest and most security-sensitive migration (drop/recreate unique indexes on `aiand_credentials` + external keys, AAD contract change breaks decryptability, telemetry lacks subject column, `credential_subjects` is a managed control-plane projection — reusing it for self-serve login violates its documented domain contract). **Rejected** for this phase; revisit only if org-level billing becomes a requirement.

### C. Signed-cookie-only sessions, no tables

HMAC cookie with `{account, installation, exp}` claims, no DB session.

- Pros: no session table.
- Cons: no revocation (logout can't kill a stolen cookie), secret management replaces the password problem, no "logout everywhere". **Rejected**; DB-backed sessions are cheap and revocable.

**Chosen: A** — with the session half of C's cookie UX (opaque token, no claims in the cookie).

## Design

### Bug fixes first (pre-req, shippable independently)

These are the diagnosed re-auth failures, to land before/with the login reform:

1. **Nil refresh ciphertext → 23502.** `internal/auth/aiand_credential_service.go` declares `var refreshCipher []byte`; for paste (and any token response without `refresh_token`) it stays nil; pgx encodes nil as NULL; column is NOT NULL. Fix: normalize nil → `[]byte{}` (empty bytea sentinel, as the query comment already promises) at the service boundary; add adapter-level normalization in `internal/postgres/aiand_credential_repo.go` as defense in depth. Regression: live-DB script under `scripts/` (ROUTER_TEST_DATABASE_URL pattern) covering paste upsert (nil refresh → success, stored `''`) and device upsert (refresh preserved).
2. **Slow-down contract drift.** Backend emits 429 + `Retry-After` header + `retry_after_seconds` body; frontend `api.ts` only special-cases 428 and dies on 429 (terminal error, polling stops). Test named `SlowDownIs428WithRetryAfter` asserts 429. **Decision (fixed, not the builder's choice): backend switches to 428** (`http.StatusPreconditionRequired`), matching its own handler comment and test name; the `Retry-After` header stays the channel, the redundant body field is dropped; the test is corrected; frontend `api.ts` 428 handling then works as written. Backend + frontend halves ship together in phase 1 — one contract, not two shippable items.
3. **Pending lifecycle.** `PollAiandDeviceLogin` clears pending *before* `completeAiandCredential` → transient failure = permanent 409 on retry; `DisconnectAiandCredential` doesn't clear an in-flight pending → stale approval can silently reconnect. Fix: an attempt is *consumed* only after the full persistence transaction succeeds (failed poll leaves it pollable); *cleared* on disconnect. Consumed attempts are retained until GC for audit.
4. **Credential + BYOK propagation not atomic.** `completeAiandCredential` upserts credential then external key; failure between leaves a credential with no dispatch key. Fix: single transaction (repo method spanning both writes) or reorder-and-reconcile.
5. **Observability.** Log the poll outcome (never token material); surface `verification_uri_complete` in the panel (backend returns it, frontend drops it). Post-disconnect device re-auth stall in the live trace never reached persistence — every poll returned pending in ≤43ms; needs the poll-outcome log + `verification_uri_complete` link to close the remaining ambiguity (user approving a superseded code).
6. **Migration shape.** Migration files wrap all DDL in `BEGIN;` … `COMMIT;` per `db/CLAUDE.md`; the down migration uses plain `DROP TABLE` (no `IF EXISTS`), tables in reverse dependency order.

### DB (migration 0143 + queries)

```sql
-- 0143_account-auth.up.sql
CREATE TABLE router.accounts (
  id              VARCHAR(36) PRIMARY KEY,        -- 'acct_' + 24 random chars (GenerateID)
  aiand_user_id   VARCHAR(128) NOT NULL,
  aiand_org_id    VARCHAR(128) NOT NULL,
  email           VARCHAR(254),
  display_name    VARCHAR(255),
  installation_id UUID NOT NULL UNIQUE REFERENCES router.model_router_installations(id),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_login_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at      TIMESTAMPTZ
);
CREATE UNIQUE INDEX accounts_identity_active_unique
  ON router.accounts (aiand_user_id, aiand_org_id) WHERE deleted_at IS NULL;

CREATE TABLE router.account_sessions (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id      UUID NOT NULL REFERENCES router.accounts(id),
  token_hash      VARCHAR(64) NOT NULL UNIQUE,    -- SHA-256 hex of opaque cookie token
  token_prefix    VARCHAR(12) NOT NULL,           -- display only, same 8+4 convention as API keys
  issued_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at      TIMESTAMPTZ NOT NULL,
  revoked_at      TIMESTAMPTZ
);
CREATE INDEX account_sessions_account_idx ON router.account_sessions (account_id, issued_at DESC);
CREATE INDEX account_sessions_expiry_idx ON router.account_sessions (expires_at) WHERE revoked_at IS NULL;

CREATE TABLE router.account_login_attempts (
  id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  device_code_ciphertext  BYTEA NOT NULL,          -- Tink-sealed; AAD (attempt-id string, "aiand-device"), same Encryptor AAD shape as aiand_credentials (id string + label)
  gateway_interval_seconds INT NOT NULL,
  expires_at              TIMESTAMPTZ NOT NULL,
  consumed_at             TIMESTAMPTZ,
  created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Expired-attempt GC: `DeleteExpiredLoginAttempts(now)` query, called from the existing hourly `runAiandCredentialRotationSweep` — no new scheduler.

Down migration drops the three tables in reverse order. No changes to `aiand_credentials`, `model_router_installations`, or existing indexes; no data backfill; `__router_admin__` row untouched.

New queries (`db/queries/accounts.sql`): `UpsertAccountOnLogin`, `GetAccountByIdentity`, `GetAccountByID`, `SoftDeleteAccount`; (`db/queries/account_sessions.sql`): `InsertAccountSession`, `GetActiveSessionByTokenHash` (join account + installation, `revoked_at IS NULL AND expires_at > now()`), `TouchSessionLastSeen`, `RevokeSessionByTokenHash`, `RevokeAllSessionsForAccount`; (`db/queries/account_login_attempts.sql`): `InsertLoginAttempt`, `GetActiveLoginAttempt`, `MarkLoginAttemptConsumed`, `DeleteLoginAttempt`; plus `GetInstallationByID` in `db/queries/model_router_installations.sql` (doesn't exist today; session middleware needs the full installation row). Regenerate SQLC.

Rationale for `(aiand_user_id, aiand_org_id)` key: stable ids from `GET /api/user` and the token's org / `X-Org-Id` probe (prior branch established `/api/v1/me` is JWT-only); email is display-only. Installation creation is idempotent under concurrent first logins via `accounts.installation_id UNIQUE` + account identity unique index; external_id = account id, name = account id (no mutable display name in the unique tiebreaker). Concurrent same-identity logins: the loser of the unique-index arbitration re-reads by identity and reuses the winner's account/installation (same create-or-relist shape as the retired `EnsureAdminInstallation`) — no 409 reaches the user. One pasted `sk-` key resolves to exactly one `(user, org)` pair → one account; two workspaces only arise from two keys in two different orgs.

### Backend

1. **Domain + adapters.** `internal/auth/account.go`: `Account`, `LoginSession`, `AiandIdentityVerifier` interface (`VerifyKey(key) (AiandIdentity, error)` returning user id/email/org), `AccountRepository`, `SessionRepository`, `LoginAttemptRepository` interfaces. `internal/aiandauth` implements the verifier (adapter); `internal/postgres` implements the repos (SQLC). Wiring in `cmd/router/main.go` only.
2. **Login service** — a **separate `auth.LoginService`** composed in `cmd/router/main.go`, NOT more methods on `auth.Service` (already a god-object; and its admin axis is deleted by this same reform — parallel agents would collide in one file): `NewLoginService(accounts, sessions, attempts, installations, credentials, externalKeys, gateway, encryptor, now, notifier)`. Owns `StartDeviceLogin(ctx)` → sealed attempt row, returns code/URIs/attempt id; `PollDeviceLogin(ctx, attemptID)` → gateway poll, on authorized one `LoginUnitOfWork` transaction: upsert account, ensure installation (idempotent), write aiand credential + external key, consume attempt, mint session; `LoginWithKey(ctx, key)` → paste path, same transaction; `Logout(token)` → revoke row; `VerifySessionToken(token)` → session + account + installation. Poll binds the attempt id from the JSON body (`{"login_id":"..."}`); no cookie pre-session. `LoginUnitOfWork` is a one-method interface in `auth`, implemented in `internal/postgres` as a `pgx.Tx` wrapper over SQLC queries.
3. **Device flow state** moves from the in-memory `aiandPending` map (installation-keyed — wrong under SaaS, unsuitable pre-installation) to the `account_login_attempts` table keyed by unguessable attempt id. Single-replica in-memory map deleted.
4. **Middleware.** `WithAccountSession` replaces `WithAdminOnly`/`WithAdminOrAuth` cookie paths: cookie → token hash → active session via a narrow `auth.SessionVerifier` interface (never `*auth.Service`, never repos directly) → set `InstallationFrom` + new `AccountFrom` on gin ctx; short-TTL LRU positive cache for session lookups. Bearer `rk_`/`ra_` still accepted for the metrics group (separate `WithAccountSessionOrAPIKey` constructor — no bool flags). Account cookies never authenticate `/v1/*`; `rk_`/`ra_` never authenticate dashboard mutations. Cookie `Max-Age` is written from the session row's `expires_at`; expiry is **sliding** — `TouchSessionLastSeen` on verified requests extends both row and cookie, so mid-use expiry is rare.
5. **Handlers/routes** — new `internal/api/account` package, new public namespace **`/account/v1/*`** (selfhosted block; per-IP rate limit on paste/start). `/admin/v1` becomes purely "installation settings behind `WithAccountSession`" — the boundary is checkable by prefix in `server.go` and tests:
   - `POST /account/v1/auth/device/start`, `POST /account/v1/auth/device/poll`, `POST /account/v1/auth/paste` — unauthenticated.
   - `GET /account/v1/auth/me` — session → `{authenticated, email, org_name, installation}`; 200 `{authenticated:false}` when unauthenticated (never a 401 redirect trap). `POST /account/v1/auth/logout` — revoke row + clear cookie.
   - Existing `/admin/v1/aiand-auth/*` stays as the settings-surface for the *upstream connection* (auto-provisioned at login; panel remains for disconnect/reconnect), moves under `WithAccountSession`. Disconnect revokes the upstream credential + external key and **keeps the dashboard session** (session identity is the account, not the credential; the dashboard still renders, serving stops, the panel says so). Reconnect replaces the credential through the fixed completion path.
   - `resolveInstallation` drops the `EnsureAdminInstallation` fallback entirely.
   - `metricsScope` drops the all-installations branch: session → its installation only.
6. **Delete the admin axis**: `WithAdminPassword`, `AdminLoginEnabled`, `VerifyAdminPassword*`, `IssueAdminSession`, `VerifyAdminSession`, `AdminPrincipal`, cookie/TTL constants, failure LRU, `admin.go` login handlers, `cmd/router/main.go` password wiring, `internal/auth/admin_test.go` (11 tests), `ROUTER_ADMIN_PASSWORD` everywhere. `EnsureAdminInstallation`/`AdminInstallationExternalID` constants: delete from `internal/auth`; update the two consumers (`cmd/seed/main.go`, `scripts/rotate_key_race_ext_check`) to their own literals or the seed script is retired in favor of login-bootstrap. **Phase 3 also closes the pre-existing layering breach**: `internal/auth/aiand_credential_service.go` imports `internal/aiandauth` (an HTTP adapter) — the `AiandGateway` interface + token/identity DTOs move into `internal/auth`, `internal/aiandauth` keeps only the HTTP client implementation, `cmd/router/main.go` wires them. Part of this reform, not deferred.
7. **Rate limiting**: per-IP limiter on the public login endpoints (reuse the LRU pattern being deleted from admin.go — move, don't reinvent).
8. **Rotation sweep**: unchanged; `aiand_credentials` UNIQUE(installation_id) stays, sweep lists expiring rows, rotates per installation, and GCs expired login attempts.

### Frontend

UX decisions (from brainstorm):

1. `login/page.tsx`: **device flow auto-starts on mount as the primary surface; "Use an API key instead" is a text link** (deep-link `/login?mode=paste`) — no tabs, no wizard. Device screen: large copyable code + "Open Aiand to approve" button (`verification_uri_complete`, new tab), manual fallback line ("or enter the code at the Aiand device page"), expiry countdown, `aria-live` waiting state, Cancel; terminal states (expired/denied) keep the card and offer "Get a new code". Paste screen: inline `role=alert` errors, value preserved, refocus. First-run copy: "Signing in creates your own isolated router workspace." Attempt id survives reloads (`sessionStorage`); every login-path fetch passes `authRedirect:false`.
2. `api.ts`: base URL splits (`/account/v1` for auth, `/admin/v1` for settings); `auth.login(password)` removed; add start/poll/paste/logout/me; 428 handling per the fixed contract; surface `verification_uri_complete`; `me()` never triggers the 401 bounce.
3. `(app)/layout.tsx` gate unchanged in shape; logout broadcasts a `storage` event so sibling tabs bounce immediately.
4. `AiandAccountPanel` stays as the upstream-connection manager: disconnect via inline destructive confirm card (not `window.confirm`) stating "routing stops immediately; your `rk_` keys stay valid but requests fail"; persistent disconnected banner in the app layout; dashboard empty state splits "no traffic yet" vs "serving is off"; `RouterOnboarding` suppressed while disconnected and its `localStorage` skip key namespaced by installation id (today it leaks across users on a shared browser).
5. "Signed in as <email>" surfaces at the top of `/settings`; sidebar sign-out gains `aria-label` and a labelled row on `md+`. Remove password-specific error strings and the `subject: "admin"` assumptions.

### Bootstrap (first-ever interaction)

Fresh deployment, no rows: visitor hits `/ui/login` → device start/paste → identity resolved → one transaction: account + installation (external_id = account id, name = account id) + `aiand_credentials` (with empty-refresh fix) + `aiand` external key + session → cookie → dashboard. No env vars, no seed, no admin password. Existing `__router_admin__` deployments: legacy row is dormant; owner logs in, gets a new account/installation; legacy keys should be revoked at cutover (documented runbook step, not automatic).

### Isolation invariants (test matrix)

- IDOR: A's cookie vs B's installation across metrics, keys, provider keys, aiand status/disconnect, onboarding → all 404/401.
- Concurrent first logins for same identity → one account, one installation.
- Paste-after-disconnect persists (the regression).
- Account cookie never authorizes `/v1/*`; `rk_`/`ra_` never authorize `/admin/v1/*` mutations.
- Logout revokes the row; stolen cookie dies on logout; disconnect does not strand sessions.
- Device poll slow-down keeps polling (428 contract).
- Two users in different orgs → two installations, zero shared rows.

### Sequencing (implementation phases)

Ticket DAG (published to GitHub as issues; first wave runs 5 agents in parallel):

1. **T1** nil-refresh cipher fix + live-DB regression script — no blockers.
2. **T2** slow-down contract unified on 428 (backend + frontend comment) — no blockers.
3. **T3** pending lifecycle + credential/external-key single transaction + poll-outcome logging (blocked by T1).
4. **T4** auth DTO / AiandGateway prefactor, kill `internal/auth → internal/aiandauth` import — no blockers.
5. **T5** migration 0143 + all queries + SQLC regen + postgres repos + `GetInstallationByID` — no blockers (only ticket touching `db/queries`).
6. **T6** `LoginService` + `LoginUnitOfWork` + fake-repo unit tests (T4, T5).
7. **T7** `internal/api/account` handlers + `/account/v1/*` routes + per-IP limiter moved from admin.go (T6).
8. **T8** `WithAccountSession` (+ session LRU cache) + `AccountFrom` + metrics variant (T6).
9. **T9** atomic flip: dashboard groups → new middleware, `aiand-auth` panel move, delete admin axis + `/admin/v1/auth/*` (T7, T8).
10. **T10** seed/script literals: `cmd/seed` local constants, `scripts/rotate_key_race_ext_check` rebase, smoke seeding (no blockers — run early).
11. **T11** `api.ts` restructure: split base URLs, drop password client, 428-safe polling client (T7).
12. **T12** login page: device flow auto-start + paste link + `verification_uri_complete` + sessionStorage attempt (T11).
13. **T13** panel disconnect UX + disconnected states + onboarding suppression/namespacing (T9, T11).
14. **T14** isolation invariant suite (T9).
15. **T15** smoke login scenario — paste login via MITM cassette (`/api/user` replay), assert cookie → `me()` → metrics scoped, `/admin/v1/auth/login` 404 (T9, T10).
16. **T16** docs + env sweep: README, CONFIGURATION, DEPLOY_BUILDIO, ui-tunnel README, CONTRIBUTING, `.env.example`, AGENTS/CLAUDE mirrors, legacy-key revocation runbook (T9).

## Decisions (settled, baked into tickets)

1. Account key: `(aiand_user_id, aiand_org_id)` — a human in two orgs gets two isolated workspaces.
2. Paste login = Aiand `sk-` key only; router `rk_`/`ra_` keys never authenticate the dashboard.
3. Disconnect keeps the dashboard session (session identity is the account, not the credential).
4. `cmd/seed` stays for dev fixtures with local literals; production bootstrap is login-driven.
5. Login namespace: new `/account/v1/*` + `internal/api/account` (prior-art pattern); `/admin/v1` = installation settings only.
6. Login service: separate `auth.LoginService`, not methods on `auth.Service`.
7. 428 slow-down contract; 7-day sliding sessions; device flow is the primary login surface with paste as a link.
8. Legacy `__router_admin__` data: dormant + documented revocation runbook, no automatic migration.

## What's left to the builder

- `LoginUnitOfWork` interface shape (defined in Backend item 2; exact method signature is builder's).
- Playwright/curl smoke of the full first-run flow against docker compose.
- CSRF posture for mutation endpoints (SameSite=Lax today; decide whether a custom-header requirement is added now or deferred).
