---
name: ufm
description: "Alias for unforce-model — clear the active Aiand Router model pin."
---

<!-- aiand-router managed ufm skill -->

When the user invokes `$ufm`, run this skill's `scripts/emit.sh` using the
absolute path shown in this skill's `<path>` (its directory + `/scripts/emit.sh`).
Do not send a user message and do not type a Codex slash command.

The script prints a leading-space `/unforce-model` line. The Aiand Router reads that
exec output, clears the pin, and continues this turn — so run the script once and
do not repeat its output as your answer. Then report the router's response.
