---
name: router-status
description: "Show whether Codex is routing through the Aiand Router or using its default provider."
---

<!-- aiand-router managed router-status skill -->

# Aiand Router status

When the user invokes `$router-status`, report whether this Codex installation
routes through the Aiand Router.

Run exactly:

```bash
npx --package @aiand-os/router -y -- aiand-router status --codex{{SCOPE}}
```

Then summarize the result in one line. Do not change any configuration.
