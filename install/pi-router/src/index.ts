/**
 * @aiand-os/router — route the pi coding agent through the Aiand Router.
 *
 * Wiring:
 *   - provider:     register `aiand` with per-process knob headers (quality on
 *                   the main loop, speed/cheap in subagents).
 *   - metadata:     stamp body.metadata.user_id for sticky sessions + subagent
 *                   detection.
 *   - Loom UI:      branded header, Wooly animation, actual route, and saved $.
 *   - safety:       block catastrophic bash (unless AIAND_NO_SAFETY=1).
 *   - compaction:   protect long tool loops and compact before model escalation.
 *   - dispatch:     parallel, context-isolated subagents — top-level process
 *                   only (no grandchildren).
 *   - lsp:          code intelligence (definition/references/hover/symbols/
 *                   diagnostics) over lazily spawned language servers, shared
 *                   with subagents through a parent-side broker socket.
 *
 * The same module loads in dispatched children via `-e <self>`; AIAND_PI_SUBAGENT
 * flips the provider knobs and suppresses the dispatch tool so fan-out doesn't recurse.
 */

import { fileURLToPath } from "node:url";
import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { registerBetaCommand } from "./beta.js";
import { registerClassifierThread } from "./classifier-thread.js";
import { isSubagent } from "./config.js";
import { registerCompaction } from "./compaction.js";
import { registerEscalationCompaction } from "./escalation.js";
import { registerDispatch } from "./dispatch.js";
import { registerForceModelCommands } from "./force-model.js";
import { registerLsp } from "./lsp.js";
import { registerMetadata } from "./metadata.js";
import { registerRoutedModel } from "./routed-model.js";
import { registerSafety } from "./safety.js";
import { registerAiand } from "./provider.js";
import { registerWooly } from "./ui.js";

const SELF_PATH = fileURLToPath(import.meta.url);

export default function (pi: ExtensionAPI): void {
	// Register at load so the provider is available for `--list-models` and
	// print mode (dispatched children), and again on session_start so the right
	// knob headers survive `/reload` and new/resumed sessions.
	registerAiand(pi);
	pi.on("session_start", () => registerAiand(pi));

	registerMetadata(pi);
	const classifierThreadActive = registerClassifierThread(pi);
	registerBetaCommand(pi);
	registerForceModelCommands(pi);
	registerRoutedModel(pi);
	if (!isSubagent()) registerWooly(pi);
	let handoffPending = () => false;
	registerCompaction(pi, undefined, () => handoffPending() || classifierThreadActive());
	handoffPending = registerEscalationCompaction(pi, undefined, undefined, classifierThreadActive);

	if (process.env.AIAND_NO_SAFETY !== "1") registerSafety(pi);

	// registerLsp picks its own role: the main process gets the pool-backed tool
	// and returns the broker provider below; a child gets the broker-backed tool
	// only when dispatch actually handed it a socket.
	const lspBroker = process.env.AIAND_PI_NO_LSP === "1" ? undefined : registerLsp(pi);

	// Only the top-level process fans out. Children (AIAND_PI_SUBAGENT=1) load
	// this same extension but get no dispatch tool, so subagents can't spawn
	// grandchildren.
	if (!isSubagent()) registerDispatch(pi, SELF_PATH, lspBroker);
}
