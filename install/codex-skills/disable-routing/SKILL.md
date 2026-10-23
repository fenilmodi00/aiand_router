---
name: disable-routing
description: "Switch Codex back to its normal provider for future sessions."
---

<!-- aiand-router managed disable-routing skill -->

# Disable Aiand routing

When the user invokes `$disable-routing`, switch the current Codex installation
off the Aiand Router without logging out or deleting its router configuration.

1. Explain that the change takes effect on the next `codex` launch and that it
   can later be reversed with `npx --package @aiand-os/router -y -- aiand-router on --codex{{SCOPE}}`.
2. Run exactly:

   ```bash
   npx --package @aiand-os/router -y -- aiand-router off --codex{{SCOPE}}
   ```

3. Report the command result. Do not uninstall the router or alter any other
   Codex settings.
