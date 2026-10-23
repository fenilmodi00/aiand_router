---
name: router-models
description: "List the models the Aiand Router may route to, and turn them on or off."
---

<!-- aiand-router managed router-models skill -->

# Aiand Router model selection

When the user invokes `$router-models`, show which models this
installation lets the router pick from, and change that selection when asked.
This is the same list — and the same stored setting — as the checkboxes on the
router dashboard's settings page.

Start by running:

```bash
npx --package @aiand-os/router -y -- aiand-router models --codex{{SCOPE}}
```

That prints every deployed model grouped by provider, with `[x]` for models the
router may pick and `[ ]` for models it may not. Present it back as a compact
checklist in that same `[x]` / `[ ]` form, keeping the provider grouping and the
exact model ids — the user selects models by id.

Then:

- If the user named models or providers, work out whether they want them on or
  off from how they phrased it, and apply it with
  `aiand-router models enable <id>... --codex{{SCOPE}}` or
  `aiand-router models disable <id>... --codex{{SCOPE}}` (add `providers` before
  `enable`/`disable` to switch a whole provider). Several ids can go in one
  call. Then re-run the list and show the result.
- If they named nothing, stop after the list and ask which ones to change.
  Never change anything they did not ask for.

Other things they might ask for:

- Rank models by preference:
  `aiand-router models prefer <id> <id>... --codex{{SCOPE}}` (order matters), or
  `aiand-router models prefer clear --codex{{SCOPE}}` to drop the ranking.
- Providers only: `aiand-router models providers --codex{{SCOPE}}`.

If the command reports that this router doesn't expose model selection, that's a
Aiand-hosted router: model selection belongs to the whole organization there, so
tell the user to change it at https://router.workaiand.ai/models —
don't try to work around it. Its listing carries no on/off state, so present it
as a plain list; don't infer which models are enabled.

Disabling a model takes effect on the router's next routing decision; no restart
is needed.
