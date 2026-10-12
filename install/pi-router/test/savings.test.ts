import assert from "node:assert/strict";
import test from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";
import { registerRoutedModel } from "../src/routed-model.js";
import {
	aggregateSavings,
	createSavingsEntry,
	formatSavings,
	isSavingsEntryData,
	normalizeModelId,
	SAVINGS_ENTRY_TYPE,
} from "../src/savings.js";

const usage = { input: 1_000_000, output: 0, cacheRead: 0, cacheWrite: 0 };

test("normalizes Pi, dated, and context-tier model ids", () => {
	assert.equal(normalizeModelId("weave/zai-org/glm-5.3[1m]"), "zai-org/glm-5.3");
	assert.equal(normalizeModelId("zai-org/glm-5.3-20260101"), "zai-org/glm-5.3");
});

test("calculates savings against identical usage", () => {
	const entry = createSavingsEntry(
		{ requestedModel: "zai-org/glm-5.3", routedModel: "moonshotai/kimi-k3" },
		usage,
	);
	assert.equal(entry.priced, true);
	assert.equal(entry.requestedCostUsd, 3);
	assert.equal(entry.routedCostUsd, 0.95);
	assert.ok(Math.abs((entry.savingsUsd ?? 0) - 2.05) < 1e-12);
});

test("applies cache write and cache read multipliers", () => {
	const entry = createSavingsEntry(
		{ requestedModel: "deepseek-ai/deepseek-v4-flash", routedModel: "moonshotai/kimi-k3" },
		{ input: 100, output: 50, cacheRead: 100, cacheWrite: 100 },
	);
	const requestedInput = 100 + 1.25 * 100 + 0.25 * 100;
	const routedInput = 100 + 1.25 * 100 + 0.1 * 100;
	const expectedRequested = (requestedInput * 2 + 50 * 6) / 1_000_000;
	const expectedRouted = (routedInput * 1 + 50 * 5) / 1_000_000;
	assert.equal(entry.requestedCostUsd, expectedRequested);
	assert.equal(entry.routedCostUsd, expectedRouted);
});

test("same-model routes have an exact zero delta without requiring a catalog price", () => {
	const entry = createSavingsEntry({ requestedModel: "private-model", routedModel: "private-model" }, usage);
	assert.equal(entry.priced, true);
	assert.equal(entry.savingsUsd, 0);
	assert.deepEqual(entry.unpricedModels, []);
});

test("unknown swapped models are explicit instead of fabricated zero savings", () => {
	const entry = createSavingsEntry(
		{ requestedModel: "zai-org/glm-5.3", routedModel: "private-model" },
		usage,
	);
	assert.equal(entry.priced, false);
	assert.equal(entry.savingsUsd, undefined);
	assert.deepEqual(entry.unpricedModels, ["private-model"]);
	assert.equal(formatSavings(aggregateSavings([entry])), "saved — · 1 unpriced");
});

test("negative deltas render as added cost", () => {
	const entry = createSavingsEntry(
		{ requestedModel: "moonshotai/kimi-k3", routedModel: "zai-org/glm-5.3" },
		usage,
	);
	assert.equal(formatSavings(aggregateSavings([entry])), "extra $4.00");
});

test("aggregates restored entry data and rejects malformed entries", () => {
	const first = createSavingsEntry(
		{ requestedModel: "zai-org/glm-5.3", routedModel: "moonshotai/kimi-k3" },
		usage,
	);
	const second = createSavingsEntry(
		{ requestedModel: "zai-org/glm-5.3", routedModel: "private-model" },
		usage,
	);
	const aggregate = aggregateSavings([first, second]);
	assert.equal(aggregate.pricedResponses, 1);
	assert.equal(aggregate.unpricedResponses, 1);
	assert.equal(aggregate.lastEntry, second);
	assert.equal(formatSavings(aggregate), "saved $2.05 · 1 unpriced");
	assert.equal(isSavingsEntryData(first), true);
	assert.equal(isSavingsEntryData({ ...first, usage: { input: -1 } }), false);
});

test("persists routed-response savings in RPC mode", async () => {
	type CapturedHandler = (event: unknown, ctx: ExtensionContext) => unknown;
	const handlers = new Map<string, CapturedHandler>();
	const appended: Array<{ customType: string; data: unknown }> = [];
	const statuses: string[] = [];
	const pi = {
		on(event: string, handler: CapturedHandler) {
			handlers.set(event, handler);
		},
		appendEntry(customType: string, data: unknown) {
			appended.push({ customType, data });
		},
	} as unknown as ExtensionAPI;
	registerRoutedModel(pi);
	const ctx = {
		mode: "rpc",
		hasUI: true,
		model: { id: "zai-org/glm-5.3" },
		sessionManager: { getBranch: () => [] },
		ui: {
			notify() {},
			setStatus(_key: string, value: string | undefined) {
				if (value) statuses.push(value);
			},
		},
	} as unknown as ExtensionContext;

	await handlers.get("session_start")?.({ type: "session_start" }, ctx);
	await handlers.get("after_provider_response")?.(
		{
			type: "after_provider_response",
			status: 200,
			headers: {
				"x-router-model": "moonshotai/kimi-k3",
				"x-router-provider": "aiand",
				"x-router-decision": "automatic",
			},
		},
		ctx,
	);
	assert.equal(statuses.at(-1), "WEAVE ROUTER — moonshotai/kimi-k3 ← zai-org/glm-5.3 · saved —");
	await handlers.get("turn_end")?.(
		{
			type: "turn_end",
			message: {
				role: "assistant",
				model: "zai-org/glm-5.3",
				usage,
			},
		},
		ctx,
	);

	assert.equal(appended.length, 1);
	const saved = appended[0];
	assert.ok(saved);
	assert.equal(saved.customType, SAVINGS_ENTRY_TYPE);
	assert.ok(isSavingsEntryData(saved.data));
	assert.equal(saved.data.routedModel, "moonshotai/kimi-k3");
	assert.equal(saved.data.provider, "aiand");
	assert.equal(saved.data.decision, "automatic");
	assert.equal(statuses.at(-1), "WEAVE ROUTER — moonshotai/kimi-k3 ← zai-org/glm-5.3 · saved $2.05");
});
