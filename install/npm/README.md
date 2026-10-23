# @aiand-os/router

One command, anywhere, to point Claude Code, Codex, opencode, or pi at the Aiand Router.

The package was renamed from `@workaiand/router`. The old package remains
published as a compatibility alias and continues to receive the same releases;
it prints a migration warning directing users to `@aiand-os/router`.

```bash
npx @aiand-os/router                       # open the hosted setup page
npx @aiand-os/router --claude              # skip the picker, target Claude Code
npx @aiand-os/router --codex               # skip the picker, target the OpenAI Codex CLI
npx @aiand-os/router setup --claude --codex # configure both native clients
npx @aiand-os/router status                 # connectivity, native configs, account health
npx @aiand-os/router --opencode            # skip the picker, target opencode
npx @aiand-os/router --pi                  # skip the picker, target pi + Loom UI
npx @aiand-os/router --scope project       # per-repo install; key-bearing config stays out of git
npx @aiand-os/router --local               # self-hosted via docker-compose (localhost:8080)
npx @aiand-os/router --base-url https://router.acme.internal
npx @aiand-os/router --email you@example.com # set identity without prompting
npx @aiand-os/router --claude --return-url https://app.example.com/onboarding # continue after a verified install
npx @aiand-os/router --non-interactive     # reads $AIAND_ROUTER_KEY, no prompts (defaults to claude)
```

For Claude Code the installed statusline and `/force-model`, `/router-*` slash
commands also refresh themselves in the background about once a week (never
overwriting a wrapper you edited). Opt out with `AIAND_STATUSLINE_UPDATE=0`, or
just the commands with `AIAND_COMMANDS_UPDATE=0`. Codex installs native `$` skills
plus managed `SessionStart`/`Stop` hooks: the latest routed model is reflected in
the terminal title and a compact `Aiand Router · …` status message is shown when
the router reports a new route. Existing Codex hooks are preserved. OpenCode and pi
have their own target-specific integrations.

Version-pin for reproducible setups:

```bash
npx @aiand-os/router@0.1.0 --claude --scope project
```

Switch on/off without uninstalling (keeps your config so switching back is
instant; requires an explicit client):

```bash
npx @aiand-os/router off --claude      # route Claude Code directly to Anthropic
npx @aiand-os/router on --claude       # route Claude Code through the router again
npx @aiand-os/router status --codex    # is Codex on the router or direct?
```

Claude Code reads its router setting at launch, so quit and reopen it after an
on/off. Codex and opencode pick it up on their next run. Inside Claude Code the
slash commands `/router-off`, `/router-on`, and `/router-status` do the same.
Codex installs `$router-status`, `$router-off`, `$router-on`, and
`$router-models` skills that call the same CLI verbs, plus a
`$disable-routing` skill that switches its next session back
to the normal provider; Codex does not support third-party `/disable-routing`
slash commands. The shell equivalent is `npx @aiand-os/router disable-routing`.
Cursor has no config file we own — toggle its base URL override in **Settings →
Models** instead.

Pick which models the router is allowed to route to:

```bash
npx @aiand-os/router models --claude                  # list every model, with its on/off state
npx @aiand-os/router models --codex                   # same, for a Codex install
npx @aiand-os/router models disable gpt-5.6 --claude  # take one out of rotation
npx @aiand-os/router models enable gpt-5.6 --claude   # put it back
```

Inside Claude Code that's `/router-models` (alias `/models`). Editing needs a
router that serves the model-selection API; against the Aiand-hosted router the
list still prints and points you at the dashboard, where model selection is an
organization-wide setting.

Uninstall:

```bash
npx @aiand-os/router --uninstall                       # Claude Code, user scope
npx @aiand-os/router --uninstall --codex               # Codex, user scope
npx @aiand-os/router --uninstall --opencode            # opencode, user scope
npx @aiand-os/router --uninstall --pi                  # pi, user scope
npx @aiand-os/router --uninstall --scope project       # Claude Code, inside the repo
npx @aiand-os/router --uninstall --codex --scope project
```

## What it does

On macOS and Linux, this package runs [`install.sh`](../install.sh). On
Windows, it uses the full Bash installer when Git Bash is available and
otherwise falls back to a native Node.js installer, so a fresh Windows machine
does not need Git Bash, WSL, or jq for basic setup. The no-Bash path configures
the client's router connection and supports uninstall for Claude Code, Codex,
OpenCode, and pi. It does not yet install shell-based status displays, hooks, or
slash-command assets; commands such as `status`, `update`, and `models` still
require Git Bash or WSL.

Four install targets:

- **Claude Code** (default) — patches `~/.claude/settings.json` (or
  `<repo>/.claude/settings.json` with `--scope project`) so `claude` routes
  through Aiand automatically. Anthropic plan credentials flow through to
  api.anthropic.com.
- **Codex** (`--codex`) — patches `~/.codex/config.toml` (or
  `<repo>/.codex/config.toml`) with a managed `[model_providers.aiand]`
  block plus `model_provider = "aiand"`. Fresh installs select `aiand-auto`,
  while reinstalling preserves an existing model choice. The provider preserves
  the active ChatGPT OAuth login. No install pins `X-Aiand-Router-Strategy`;
  every endpoint keeps its router's configured default. Switching models with
  Codex's native `/model` picker mid-session forces the named model and its
  selected reasoning effort; the model a session launches with stays a
  baseline that routes automatically, and selecting **Aiand Router** resumes
  per-request routing. An existing `$fm`
  session pin still applies in automatic mode until `$ufm` clears it. Re-run
  `npx @aiand-os/router --codex` and restart Codex to enable this on an older
  install. `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`, and `gpt-6-sol`
  can use the active ChatGPT plan; other selected models use their WorkAiand
  deployment or BYOK credential.
  Re-running the installer refreshes the Aiand provider, and `--uninstall --codex`
   removes it while preserving the other settings in your config. Codex does not load
   third-party slash-command files; the installer provides native skills
   `$force-model` (`$fm`), `$unforce-model` (`$ufm`), and `$router-feedback`
   (`$rf`), whose invocations are handled by the router directly. It also
   installs `$router-status`, `$router-off`, `$router-on`, and `$router-models`,
   which call this installer's own verbs, plus a `$disable-routing` skill that
   returns the next Codex session to the default provider without logging out
   or deleting the router configuration. The managed lifecycle hooks also keep
   the latest routed model in the terminal title and emit a compact status
   message when the router reports a new route.
- **opencode** (`--opencode`) — merges a Responses-format `provider.aiand`
  entry backed by `@ai-sdk/openai` into
  `~/.config/opencode/opencode.json` (or `<repo>/opencode.json` with
  `--scope project`). OpenCode major version 2 is required when the CLI is
  installed and available on `PATH`. Installation activates `aiand/auto` and
  parks any prior default model so `off` and uninstall can restore it exactly.
  The installer uses OpenCode's native Responses provider and session headers;
  no plugin is installed. Re-install rewrites only the managed provider and
  removes any stale v1 plugin registration/files while preserving unrelated
  providers, MCP servers, agents, and plugins.
- **pi** (`--pi`) — registers the `aiand` provider and installs this package as
  a pi extension. Stock pi then gets the Loom startup header, Wooly's animated
  mascot, the persistent actual-route display, cumulative session savings,
  `/fm` + `/ufm` model-pin commands with a `[forced]` status, and the
  context-isolated `dispatch` tool. There is no forked pi binary and no separate
  Loom runtime.

See the [main installer docs](https://github.com/aiand-os/router/tree/main/install)
for the full reference.

## License

Apache-2.0 starting with version `0.2.24` of both `@aiand-os/router` and
`@workaiand/router`. See the bundled `LICENSE` and `NOTICE` files.
Earlier published versions retain their original licenses. Third-party peer
dependencies are separately licensed and are not bundled in this package.

## Requirements

- Node ≥ 18 (ships with `npx`)
- Windows users can run the command from PowerShell with Node.js/npm installed;
  Git Bash/WSL is optional for basic setup
- macOS/Linux users need Bash on PATH
- `jq` is reused when available; otherwise, on macOS (arm64/x86_64), Linux (arm64/x86_64), and Windows Git Bash (x86_64), the installer downloads checksum-verified jq 1.8.1 to `~/.aiand/bin`. Other architectures need jq installed manually. No Homebrew or administrator access is needed. Downloads require access to GitHub.

## Why npx

`npx @aiand-os/router` provides one-command setup and painless version
pinning.

## Older npm

On npm ≤ 6 the bundled `npx` treats an undeclared `-y` as consuming the next
token, so `npx -y @aiand-os/router --claude` silently drops the package name
and resolves the following argument as the command instead. Either upgrade
(`npm i -g npm@latest`) or name the binary explicitly:

```bash
npx --package @aiand-os/router -y -- aiand-router --claude
```

That form is correct on every npm version.
