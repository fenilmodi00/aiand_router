---
description: Route Claude Code directly to Anthropic again (turn the Aiand Router off).
allowed-tools: Bash(npx:*)
---

Turn the Aiand Router **off** for Claude Code by running:

`npx @aiand-os/router off --claude{{SCOPE}}`

Then tell me to fully quit and reopen Claude Code — it only reads the router setting at startup, so the change won't apply to the current session until it restarts.
