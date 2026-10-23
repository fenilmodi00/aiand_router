---
name: router-on
description: "Route Codex through the Aiand Router again (turn it back on)."
---

<!-- aiand-router managed router-on skill -->

# Turn Aiand routing on

When the user invokes `$router-on`, switch this Codex installation back onto
the Aiand Router using the managed configuration already on disk.

Run exactly:

```bash
npx --package @aiand-os/router -y -- aiand-router on --codex{{SCOPE}}
```

Then report the result and tell the user the change takes effect on their next
`codex` launch — Codex reads its provider config at startup, so the current
session keeps routing as it already was. Do not re-run the installer or alter
any other Codex settings.
