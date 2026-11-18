import assert from "node:assert/strict";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import test from "node:test";
import type { ExtensionAPI, ExtensionContext } from "@mariozechner/pi-coding-agent";
import { registerAutoModel, rewriteAutoPayload } from "../src/auto-model.js";
import { AUTO_WIRE_MODEL, PROVIDER_NAME, AIAND_AUTO_MODEL_ID, AIAND_MODELS } from "../src/config.js";

type Handler = (event: any, ctx: ExtensionContext) => unknown;

function harness(argv: string[] = []) {
	const handlers = new Map<string, Handler[]>();
	const selected: unknown[] = [];
	const pi = {
		on(event: string, handler: Handler) {
			handlers.set(event, [...(handlers.get(event) ?? []), handler]);
		},
		async setModel(model: unknown) {
			selected.push(model);
			return true;
		},
	} as unknown as ExtensionAPI;
	registerAutoModel(pi, argv);
	return {
		selected,
		async emit(event: string, payload: unknown, ctx: ExtensionContext) {
			let result: unknown;
			for (const handler of handlers.get(event) ?? []) result = await handler(payload, ctx);
			return result;
		},
	};
}

interface Dirs {
	agent: string;
	cwd: string;
}

async function withDirs(globalSettings: Record<string, unknown> | undefined, callback: (dirs: Dirs) => Promise<void> | void) {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), "aiand-auto-model-"));
	const dirs = { agent: path.join(root, "agent"), cwd: path.join(root, "project") };
	fs.mkdirSync(dirs.agent);
	fs.mkdirSync(dirs.cwd);
	if (globalSettings) fs.writeFileSync(path.join(dirs.agent, "settings.json"), JSON.stringify(globalSettings));
	const previous = process.env.PI_CODING_AGENT_DIR;
	process.env.PI_CODING_AGENT_DIR = dirs.agent;
	try {
		await callback(dirs);
	} finally {
		if (previous === undefined) delete process.env.PI_CODING_AGENT_DIR;
		else process.env.PI_CODING_AGENT_DIR = previous;
		fs.rmSync(root, { recursive: true, force: true });
	}
}

function readJson(file: string): Record<string, unknown> {
	return JSON.parse(fs.readFileSync(file, "utf-8"));
}

const autoModel = { provider: PROVIDER_NAME, id: AIAND_AUTO_MODEL_ID };

type ModelRef = { provider: string; id: string };

function context(model: ModelRef | undefined, cwd: string, options: { branch?: unknown[]; scopedModels?: ModelRef[] } = {}) {
	return {
		cwd,
		model,
		scopedModels: (options.scopedModels ?? []).map((scoped) => ({ model: scoped })),
		sessionManager: { getBranch: () => options.branch ?? [] },
		modelRegistry: { find: (provider: string, id: string) => (provider === PROVIDER_NAME && id === AIAND_AUTO_MODEL_ID ? autoModel : undefined) },
	} as unknown as ExtensionContext;
}

function writeProjectSettings(cwd: string, settings: Record<string, unknown>) {
	fs.mkdirSync(path.join(cwd, ".pi"));
	fs.writeFileSync(path.join(cwd, ".pi", "settings.json"), JSON.stringify(settings));
}

const anthropicModel = { provider: "anthropic", id: "claude-opus-5-5" };
const retiredDefault = { defaultProvider: PROVIDER_NAME, defaultModel: "claude-opus-4-8" };

function resumedOn(provider: string, model: string) {
	return [{ type: "message", message: { role: "user", content: "hi" } }, { type: "message", message: { role: "assistant", provider, model, content: [] } }];
}

test("the aiand provider offers only the Aiand Router entry", () => {
	assert.deepEqual(AIAND_MODELS.map((model) => [model.id, model.name]), [[AIAND_AUTO_MODEL_ID, "Aiand Router"]]);
});

test("an auto request carries the wire model and other requests are untouched", async () => {
	const extension = harness();
	const rewritten = await extension.emit("before_provider_request", { payload: { model: AIAND_AUTO_MODEL_ID, max_tokens: 1 } }, context(autoModel, os.tmpdir()));
	assert.deepEqual(rewritten, { model: AUTO_WIRE_MODEL, max_tokens: 1 });
	const otherProvider = await extension.emit("before_provider_request", { payload: { model: AIAND_AUTO_MODEL_ID } }, context({ provider: "other", id: AIAND_AUTO_MODEL_ID }, os.tmpdir()));
	assert.equal(otherProvider, undefined);
	assert.equal(rewriteAutoPayload({ model: "claude-opus-5-5" }), undefined);
	assert.equal(rewriteAutoPayload({ messages: [] }), undefined);
	assert.equal(rewriteAutoPayload(null), undefined);
});

test("moves a session on a retired per-model entry to aiand/auto", async () => {
	await withDirs(undefined, async ({ cwd }) => {
		const extension = harness();
		await extension.emit("session_start", { type: "session_start" }, context({ provider: PROVIDER_NAME, id: "grok-4.7" }, cwd));
		await extension.emit("session_start", { type: "session_start" }, context(autoModel, cwd));
		await extension.emit("session_start", { type: "session_start" }, context({ provider: "anthropic", id: "claude-opus-5-5" }, cwd));
		assert.deepEqual(extension.selected, [autoModel]);
	});
});

test("rewrites a retired aiand default in place and keeps every other setting", async () => {
	await withDirs({ defaultProvider: PROVIDER_NAME, defaultModel: "claude-opus-4-8", packages: ["npm:user-package"], theme: "dark" }, async ({ agent, cwd }) => {
		const extension = harness();
		await extension.emit("session_start", { type: "session_start" }, context(autoModel, cwd));
		assert.deepEqual(readJson(path.join(agent, "settings.json")), {
			defaultProvider: PROVIDER_NAME,
			defaultModel: AIAND_AUTO_MODEL_ID,
			packages: ["npm:user-package"],
			theme: "dark",
		});
	});
});

test("leaves another provider's default untouched", async () => {
	const settings = { defaultProvider: "anthropic", defaultModel: "claude-opus-5-5" };
	await withDirs(settings, async ({ agent, cwd }) => {
		await harness().emit("session_start", { type: "session_start" }, context({ provider: "anthropic", id: "claude-opus-5-5" }, cwd));
		assert.deepEqual(readJson(path.join(agent, "settings.json")), settings);
	});
});

test("rewrites the project file that supplies a retired model under a global aiand provider", async () => {
	await withDirs({ defaultProvider: PROVIDER_NAME, defaultModel: AIAND_AUTO_MODEL_ID }, async ({ agent, cwd }) => {
		writeProjectSettings(cwd, { defaultModel: "grok-4.7" });
		await harness().emit("session_start", { type: "session_start" }, context(autoModel, cwd));
		assert.deepEqual(readJson(path.join(cwd, ".pi", "settings.json")), { defaultModel: AIAND_AUTO_MODEL_ID });
		assert.deepEqual(readJson(path.join(agent, "settings.json")), { defaultProvider: PROVIDER_NAME, defaultModel: AIAND_AUTO_MODEL_ID });
	});
});

test("keeps a retired global model a project inherits under another provider", async () => {
	await withDirs(retiredDefault, async ({ agent, cwd }) => {
		writeProjectSettings(cwd, { defaultProvider: "anthropic" });
		const extension = harness();
		await extension.emit("session_start", { type: "session_start" }, context(anthropicModel, cwd));
		assert.deepEqual(readJson(path.join(agent, "settings.json")), retiredDefault);
		assert.deepEqual(extension.selected, []);
	});
});

test("returns to aiand/auto when Pi fell back from a retired aiand default", async () => {
	await withDirs(retiredDefault, async ({ cwd }) => {
		const extension = harness();
		await extension.emit("session_start", { type: "session_start" }, context(anthropicModel, cwd));
		assert.deepEqual(extension.selected, [autoModel]);
	});
});

test("returns to aiand/auto when a resumed aiand conversation fell back", async () => {
	await withDirs({ defaultProvider: "anthropic", defaultModel: "claude-opus-5-5" }, async ({ cwd }) => {
		const extension = harness();
		await extension.emit("session_start", { type: "session_start" }, context(anthropicModel, cwd, { branch: resumedOn(PROVIDER_NAME, "grok-4.7") }));
		assert.deepEqual(extension.selected, [autoModel]);
	});
});

test("keeps another provider chosen at launch, in scope, or by the resumed conversation", async () => {
	await withDirs(retiredDefault, async ({ cwd }) => {
		const flagged = harness(["node", "pi", "--model", "anthropic/claude-opus-5-5"]);
		await flagged.emit("session_start", { type: "session_start" }, context(anthropicModel, cwd));
		const scoped = harness();
		await scoped.emit("session_start", { type: "session_start" }, context(anthropicModel, cwd, { scopedModels: [anthropicModel] }));
		const resumed = harness();
		await resumed.emit("session_start", { type: "session_start" }, context(anthropicModel, cwd, { branch: resumedOn("anthropic", "claude-opus-5-5") }));
		assert.deepEqual([...flagged.selected, ...scoped.selected, ...resumed.selected], []);
	});
});
