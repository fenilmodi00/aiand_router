/**
 * Registers the `aiand` provider with the per-process header policy.
 *
 * Re-registered on each `session_start` (and once at load) so the right knob
 * headers are always live and the provider survives `/reload`. We register a
 * new provider named "aiand" rather than overriding the built-in "anthropic"
 * provider — overriding "anthropic" would hijack the Claude OAuth token.
 */

import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import {
	getRole,
	getRouterBaseUrl,
	isSubagent,
	PROVIDER_NAME,
	providerHeaders,
	resolveRouterKey,
	AIAND_MODELS,
} from "./config.js";

export interface ContextWindowOverride {
	modelId: string;
	contextWindow: number;
}

function modelsForContextWindow(override?: ContextWindowOverride): typeof AIAND_MODELS {
	if (!override) return AIAND_MODELS;
	return AIAND_MODELS.map((model) =>
		model.id === override.modelId ? { ...model, contextWindow: override.contextWindow } : model,
	);
}

/** Register the Aiand provider, optionally using the router-confirmed active context window. */
export function registerAiand(pi: ExtensionAPI, contextWindowOverride?: ContextWindowOverride): void {
	const key = resolveRouterKey();
	const role = getRole();

	if (!key) {
		// The main loop can still run off the installer-written models.json
		// provider. A subagent MUST apply the speed/cheap knobs, so a missing
		// key there is fatal rather than silently routing on quality knobs.
		if (isSubagent()) {
			throw new Error(
				"Aiand Router: no router key found (set AIAND_ROUTER_KEY or write ~/.pi/agent/.aiand_router_key).",
			);
		}
		return;
	}

	pi.registerProvider(PROVIDER_NAME, {
		name: "Aiand Router",
		// Root URL, no /v1: the anthropic-messages provider uses @anthropic-ai/sdk,
		// which appends /v1/messages to baseUrl. A /v1 here yields /v1/v1/messages.
		baseUrl: getRouterBaseUrl(),
		// Planted to satisfy pi's "is auth configured" check. The router ignores
		// it (auth runs off X-Aiand-Router-Key); authHeader:false keeps
		// Authorization free for BYOK.
		apiKey: key,
		api: "anthropic-messages",
		authHeader: false,
		headers: providerHeaders(role, key),
		models: modelsForContextWindow(contextWindowOverride),
	});
}
