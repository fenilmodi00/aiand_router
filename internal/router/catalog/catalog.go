// Package catalog is the single source of truth for per-model data: tier,
// per-provider upstream IDs, per-provider pricing. Adding a model is one
// struct literal here. Every model carries an ordered Providers list; the
// first binding whose Provider is in the deploy's available set is chosen.
// Pure inner-ring, no I/O.
package catalog

import (
	"weave-os/router/internal/providers"
)

// Tier is the coarse capability bucket. Higher is stronger; integer
// ordering is load-bearing (planner compares freshTier > pinTier).
type Tier int

const (
	TierUnknown Tier = iota // Zero value; absent from table.
	TierLow
	TierMid
	TierHigh
)

// String returns a snake_case label for logs and OTel attrs.
func (t Tier) String() string {
	switch t {
	case TierLow:
		return "low"
	case TierMid:
		return "mid"
	case TierHigh:
		return "high"
	default:
		return "unknown"
	}
}

// Pricing holds the per-1M-token USD costs for a single (provider, model)
// binding.
type Pricing struct {
	InputUSDPer1M  float64
	OutputUSDPer1M float64
	// CacheWriteMultiplier is the cache-creation price relative to base input price.
	// Zero means unspecified — existing production pricing is preserved.
	CacheWriteMultiplier float64
	// CacheReadMultiplier is the cost of a cache hit relative to the base
	// input price (e.g. 0.10 for Anthropic, 0.50 for OpenAI). Zero means
	// "unspecified — use DefaultCacheReadMultiplier".
	CacheReadMultiplier float64
	// LongContext applies alternate rates above a provider's prompt-size
	// threshold.
	LongContext *LongContextPricing
}

// LongContextPricing holds alternate rates for a provider's long-context tier.
type LongContextPricing struct {
	ThresholdTokens      int
	InputUSDPer1M        float64
	OutputUSDPer1M       float64
	CacheWriteMultiplier float64
	CacheReadMultiplier  float64
}

// DefaultCacheReadMultiplier is the fallback multiplier for bindings
// without published cache pricing. 0.5 is conservative: high enough to not
// treat unknown providers as free caching, low enough to not block switches.
const DefaultCacheReadMultiplier = 0.5

// DefaultCacheWriteMultiplier preserves the legacy production cache-creation
// calculation until provider/model/retention-specific data is verified.
const DefaultCacheWriteMultiplier = 1.25

// EffectiveCacheReadMultiplier returns CacheReadMultiplier if set, else
// DefaultCacheReadMultiplier.
func (p Pricing) EffectiveCacheReadMultiplier() float64 {
	if p.CacheReadMultiplier > 0 {
		return p.CacheReadMultiplier
	}
	return DefaultCacheReadMultiplier
}

// EffectiveCacheWriteMultiplier returns the binding-specific cache-write
// multiplier when known, otherwise the legacy production multiplier.
func (p Pricing) EffectiveCacheWriteMultiplier() float64 {
	if p.CacheWriteMultiplier > 0 {
		return p.CacheWriteMultiplier
	}
	return DefaultCacheWriteMultiplier
}

// ForInputTokens returns the applicable pricing tier for a provider-reported
// prompt token count.
func (p Pricing) ForInputTokens(inputTokens int) Pricing {
	if p.LongContext == nil || inputTokens <= p.LongContext.ThresholdTokens {
		return p
	}
	long := p.LongContext
	return Pricing{
		InputUSDPer1M:        long.InputUSDPer1M,
		OutputUSDPer1M:       long.OutputUSDPer1M,
		CacheWriteMultiplier: long.CacheWriteMultiplier,
		CacheReadMultiplier:  long.CacheReadMultiplier,
	}
}

// ProviderBinding is one (provider, upstream-model-ID, price) tuple for a
// logical model. Ordered list per Model — the scorer picks the first whose
// Provider name is wired in the running deploy.
type ProviderBinding struct {
	// Provider is one of the providers.Provider* constants.
	Provider string
	// UpstreamID is the model ID the upstream API expects. Empty means
	// "same as Model.ID" (no rewrite); non-empty is rewritten at proxy time.
	UpstreamID string
	// Price is the per-provider pricing for this binding.
	Price Pricing
	// ContextWindow overrides the model-level ContextWindow for this binding.
	// Zero means inherit. Use when the served window differs by provider.
	ContextWindow int
}

// ToolUseQuality marks a model's reliability under has_tools=true turns.
// ToolUseUnknown (zero value) = no concerns recorded; ToolUseLow flags models
// that hallucinate tool calls, emit malformed tool_use blocks, or loop on the
// same tool. The scorer excludes ToolUseLow models from argmax on tool-bearing
// requests, falling back to the unfiltered pool only if that would empty it.
type ToolUseQuality int

const (
	ToolUseUnknown ToolUseQuality = iota
	ToolUseLow
)

// AgenticUse marks whether a model can reliably drive an agentic harness (the
// multi-step skill/tool-orchestration loop in Claude Code, opencode, etc).
// Stricter than ToolUseQuality: a model can emit well-formed tool calls yet
// still fail to run the harness (e.g. minimax-m3 grepped the filesystem for a
// skill instead of invoking it). AgenticLow flags models the scorer drops
// from has_tools turns, so the price dial can demote Opus to a cheaper
// harness-capable model instead of stranding the turn on the cheapest one.
type AgenticUse int

const (
	AgenticUnknown AgenticUse = iota
	AgenticLow
)

// ImageInput marks whether a model accepts image content parts.
// ImageInputUnknown (zero value) = no restriction; first-party models default
// here since they're all multimodal. ImageInputUnsupported flags text-only
// models that 4xx on image parts (e.g. GLM-5.1). The scorer excludes the
// ImageInputUnsupported set from image-bearing requests.
type ImageInput int

const (
	ImageInputUnknown ImageInput = iota
	ImageInputUnsupported
)

// ModelID is a catalog model identifier. Production lookup still uses string IDs
// because the table is data, but call sites that name a known catalog model
// should use these constants instead of repeating raw strings.
type ModelID string

const (
	// ModelIDDeepSeekV4Flash is the DeepSeek V4 Flash routing ID.
	ModelIDDeepSeekV4Flash ModelID = "deepseek/deepseek-v4-flash"
	// ModelIDDeepSeekV41Flash is the DeepSeek V4.1 Flash routing ID.
	ModelIDDeepSeekV41Flash ModelID = "deepseek/deepseek-v4.1-flash"
	// ModelIDDeepSeekV4Pro is the DeepSeek V4 Pro routing ID.
	ModelIDDeepSeekV4Pro ModelID = "deepseek/deepseek-v4-pro"
	// ModelIDDeepSeekV4Pro0813 is the dated DeepSeek V4 Pro routing ID.
	ModelIDDeepSeekV4Pro0813 ModelID = "deepseek/deepseek-v4-pro-0813"
	// ModelIDKimiK27 is the Kimi K2.7 routing ID.
	ModelIDKimiK27 ModelID = "moonshotai/kimi-k2.7"
	// ModelIDKimiK3 is the Kimi K3 routing ID.
	ModelIDKimiK3 ModelID = "moonshotai/kimi-k3"
	// ModelIDGLM52 is the GLM-5.2 routing ID.
	ModelIDGLM52 ModelID = "z-ai/glm-5.2"
	// ModelIDGLM53 is the GLM-5.3 routing ID.
	ModelIDGLM53 ModelID = "z-ai/glm-5.3"
	// ModelIDGLM53Flash is the GLM-5.3 Flash routing ID.
	ModelIDGLM53Flash ModelID = "z-ai/glm-5.3-flash"
)

func (id ModelID) String() string {
	return string(id)
}

// Model is one logical model — the unit the router decides on.
type Model struct {
	// ID is the public slash-form (or bare) model ID exposed to clients,
	// e.g. "claude-opus-4-7" or "deepseek/deepseek-v4-pro".
	ID string
	// Source is how the model's weights are published. Required on every row:
	// products sold as open-source-only read dispatch eligibility from it, and
	// an unset value is rejected by the catalog tests rather than defaulted.
	Source Source
	// Tier is the coarse capability bucket. TierUnknown excludes the model
	// from automatic routing.
	Tier Tier
	// ContextWindow is the model's total input+output token budget in tokens.
	// 0 means use catalog.DefaultContextWindow.
	ContextWindow int
	// ToolUseQuality: default ToolUseUnknown; set ToolUseLow to remove the
	// model from agentic argmax pools.
	ToolUseQuality ToolUseQuality
	// AgenticUse: default AgenticUnknown; set AgenticLow to keep the model
	// out of the price dial's agentic demotion ladder.
	AgenticUse AgenticUse
	// ImageInput: default ImageInputUnknown; set ImageInputUnsupported on
	// text-only models so the scorer keeps image-bearing turns off them.
	ImageInput ImageInput
	// Providers is the ordered fallback list. First binding whose
	// Provider name is in the available set wins. Must be non-empty.
	Providers []ProviderBinding
}

// PrimaryProvider returns the first binding's provider name. Callers that
// don't yet thread provider through (OTel emitter, billing debit hook)
// look up pricing by this.
func (m Model) PrimaryProvider() string {
	if len(m.Providers) == 0 {
		return ""
	}
	return m.Providers[0].Provider
}

// Models is the source of truth, one struct literal per model, grouped by
// family and tier. Tiered models with a registered provider are automatic
// routing targets. Strategy artifacts own selection membership: legacy cluster
// versions use their model_registry.json, while policy sidecars such as HMM
// intersect catalog targets with their own roster. This catalog controls
// pricing and dispatch for every strategy.
var Models = []Model{
	// --- ai& (aiand) served rows ---
	//
	// Every row below is dispatched through the single remaining upstream
	// provider, ai&'s OpenAI-compatible Chat Completions surface. ai& names
	// models vendor-prefixed, so each binding carries the UpstreamID rewrite;
	// the router's slash-form slug is not what the upstream publishes.

	// DeepSeek V4 natively serves 1,048,576 tokens; the 131_072 carried over
	// from V3.2 was filtering requests over ~128K (excludeContextOverflowModels
	// in proxy/service.go).
	{ID: ModelIDDeepSeekV4Flash.String(), Source: SourceOpenSource, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, AgenticUse: AgenticLow, Providers: []ProviderBinding{
		{Provider: providers.ProviderAIAND, UpstreamID: "deepseek-ai/deepseek-v4-flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.250, CacheReadMultiplier: 0.08 / 0.150}},
	}},
	// V4.1-Flash: 552B MoE (8B active prefill / 16B decode), natively
	// multimodal, 1M context.
	{ID: ModelIDDeepSeekV41Flash.String(), Source: SourceOpenSource, Tier: TierLow, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderAIAND, UpstreamID: "deepseek-ai/deepseek-v4.1-flash",
			Price: Pricing{InputUSDPer1M: 0.300, OutputUSDPer1M: 0.600, CacheReadMultiplier: 0.02 / 0.300}},
	}},
	// Untiered: the bare alias is the OLD 0423 release; the routable one is
	// the dated 0813 entry below, which is what AA actually benchmarks. Both
	// stay priced and bound so session pins and /force-model still dispatch.
	{ID: ModelIDDeepSeekV4Pro.String(), Source: SourceOpenSource, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderAIAND, UpstreamID: "deepseek-ai/deepseek-v4-pro",
			Price: Pricing{InputUSDPer1M: 1.000, OutputUSDPer1M: 2.500, CacheReadMultiplier: 0.25 / 1.000}},
	}},
	// The current V4-Pro release, and the one Artificial Analysis scores
	// (agentic index 49.56; our aa_skill_scores entry already carries
	// aa_name "DeepSeek V4 Pro 0813"). The bare deepseek/deepseek-v4-pro alias
	// above resolves to the retired 0423 build, so the HMM roster must target
	// this dated ID to route to what it was actually ranked on.
	{ID: ModelIDDeepSeekV4Pro0813.String(), Source: SourceOpenSource, Tier: TierMid, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderAIAND, UpstreamID: "deepseek-ai/deepseek-v4-pro",
			Price: Pricing{InputUSDPer1M: 1.000, OutputUSDPer1M: 2.500, CacheReadMultiplier: 0.25 / 1.000}},
	}},
	// kimi-k2.7 "Code" variant: ~30% less thinking-token usage.
	{ID: ModelIDKimiK27.String(), Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 262_144, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderAIAND, UpstreamID: "moonshotai/kimi-k2.7-code",
			Price: Pricing{InputUSDPer1M: 0.750, OutputUSDPer1M: 3.500, CacheReadMultiplier: 0.20 / 0.750}},
	}},
	// kimi-k3 is a separate price class from k2.7 ($3/$15 vs $0.75/$3.50), not
	// its successor — both stay routable. First multimodal Kimi, so unlike
	// k2.5-k2.7 it carries no ImageInputUnsupported.
	{ID: ModelIDKimiK3.String(), Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderAIAND, UpstreamID: "moonshotai/kimi-k3",
			Price: Pricing{InputUSDPer1M: 3.000, OutputUSDPer1M: 12.500, CacheReadMultiplier: 0.50 / 3.000}},
	}},
	// GLM-5.2.
	{ID: ModelIDGLM52.String(), Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderAIAND, UpstreamID: "zai-org/glm-5.2",
			Price: Pricing{InputUSDPer1M: 1.000, OutputUSDPer1M: 4.000, CacheReadMultiplier: 0.30 / 1.000}},
	}},
	// GLM-5.3: text-only (AA inputModalityImage=false). ContextWindow is
	// 1,048,576, not 1,310,720 (Cloudflare-only) — overstating causes
	// upstream 400 on overflow.
	{ID: ModelIDGLM53.String(), Source: SourceOpenSource, Tier: TierHigh, ContextWindow: 1_048_576, ImageInput: ImageInputUnsupported, Providers: []ProviderBinding{
		{Provider: providers.ProviderAIAND, UpstreamID: "zai-org/glm-5.3",
			Price: Pricing{InputUSDPer1M: 1.000, OutputUSDPer1M: 4.000, CacheReadMultiplier: 0.30 / 1.000}},
	}},
	// GLM-5.3-Flash: first native-multimodal (image+video) in the GLM-5 line —
	// ImageInput stays default. Thinking cannot be disabled.
	{ID: ModelIDGLM53Flash.String(), Source: SourceOpenSource, Tier: TierLow, ContextWindow: 1_048_576, Providers: []ProviderBinding{
		{Provider: providers.ProviderAIAND, UpstreamID: "zai-org/glm-5.3-flash",
			Price: Pricing{InputUSDPer1M: 0.150, OutputUSDPer1M: 0.500, CacheReadMultiplier: 0.03 / 0.150}},
	}},
}
