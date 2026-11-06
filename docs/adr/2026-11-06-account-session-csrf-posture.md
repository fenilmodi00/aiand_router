# ADR: CSRF posture of /account/v1 auth mutations

Status: accepted (2026-11-06)
Scope: dashboard account-auth surface (`/account/v1/auth/*`, selfhosted block only)

## Context

The SaaS login surface (#110) mints the `router_account_session` cookie
(HttpOnly, SameSite=Lax, Path=/, Secure unless `ROUTER_COOKIE_INSECURE=true`).
Its mutating endpoints — `POST /account/v1/auth/paste`, `/device/start`,
`/device/poll`, `/logout` — authenticate via ambient cookie, so a hostile page
could in principle trigger cross-site requests riding a victim's session. The
reform's design doc left the decision on adding a synchronizer-token /
custom-header requirement to the builder.

## Decision

Keep **SameSite=Lax + HttpOnly + JSON-only bodies, no custom-header
requirement**, for these routes only:

- **SameSite=Lax** already blocks the dangerous cross-site cases: a
  cross-site top-level POST (the classic form-driven CSRF shape) does not
  carry the cookie; only same-site requests and top-level GET navigations do.
  None of these endpoints are GET-mutating.
- The login-path endpoints are **pre-session or session-minting**: `paste`,
  `start`, and `poll` run before any cookie exists, so there is no ambient
  credential for a third-party page to ride. `logout` is the only
  authenticated mutation, and forcing logout is a nuisance, not a compromise.
- All bodies are `application/json` via `ShouldBindJSON`; a cross-site form
  POST cannot produce that content type without CORS preflight, which the
  router does not approve cross-origin.
- Cookie tokens are HttpOnly, so scripts cannot exfiltrate the session for a
  token-authenticated request either.

Dashboard **settings** mutations under `/admin/v1/*` (key issuance, model
config, aiand disconnect) ride the same cookie and the same reasoning applies;
they were reviewed with the isolation suite (#124) and inherit this posture.

## Rejected alternative

Requiring a custom header (e.g. `X-Requested-With`) or a synchronizer token on
every account-authenticated mutation. It closes a marginally wider attack
surface but adds a token-plumbing and expiry path to every dashboard fetch for
no concrete attack under the Lax+HttpOnly+JSON contract above. Revisit if the
dashboard ever gains a JSON-tolerant parser, a GET-mutating endpoint, or a
cross-origin embed use case.
