# Smoke suite cassettes

Recorded router→ai& interactions for the smoke suite's MITM proxy
(`smoke/mitmproxy/`). Each `<sha256>.json` file is one request/response pair,
keyed by a hash of (method, path, body) — the smoke fixtures are
byte-deterministic, so a given scenario always hashes to the same file.

**These are committed to the repo.** That's what lets a PR CI run in
`replay-only` mode read them straight off disk with no upstream API key. Every
response body here was auth-header-sanitized before being written (see
`sanitizeHeaders` in `../store.go`) — `Authorization` / `x-api-key` never make
it into a cassette.

## Refreshing

Cassettes go stale when a fixture, scenario, or ai&'s own response shape
changes. Refresh them locally:

```bash
AIAND_API_KEY=sk-… SMOKE_PROXY_MODE=record make smoke
git status smoke/mitmproxy/cassettes/   # review the diff, then commit
```

See `docs/SMOKE.md` for when refreshing is expected (a fixture/scenario change,
or a suspected upstream API shape change).

Do not hand-edit these files — regenerate them so the recorded shape matches
something the real API actually returned.

**Exception — identity cassettes.** The `GET /api/user` cassettes for the
paste-login scenario (`smoke/login_test.go`) are hand-authored: device/paste
flow is not cassette-safe to record (the pasted credential is synthetic by
contract, never live). They are keyed by (method, path, Authorization) — see
`identityPaths` in `../store.go` — so one directory carries two distinct
synthetic identities for the second-account isolation check. Every field is
synthetic (`example.invalid` emails, fixed synthetic UUID ids); nothing here
was captured from the live gateway.
