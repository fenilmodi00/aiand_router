package proxy

import (
	"strings"

	"aiand-os/router/internal/router/catalog"
)

// forceModelAliases maps a human-typed name to a catalog model ID. Every
// target must be a live catalog row: an alias naming a model the upstream
// no longer publishes would resolve to nothing and fail the request.

var forceModelAliases = unversionedAliasesFollowLatestFamilyRelease(map[string]string{
	// Claude family aliases follow GLM-5.3, the strongest text model in
	// the catalog.
	"anthropic":   "z-ai/glm-5.3",
	"claude":      "z-ai/glm-5.3",
	"opus":        "z-ai/glm-5.3",
	"claude-opus": "z-ai/glm-5.3",
	"opus-5.5":    "z-ai/glm-5.3",
	"opus-5-5":    "z-ai/glm-5.3",
	"opus5.5":     "z-ai/glm-5.3",
	"opus55":      "z-ai/glm-5.3",
	"claude-5.5":  "z-ai/glm-5.3",
	"claude-5-5":  "z-ai/glm-5.3",
	"opus-5":      "z-ai/glm-5.3",
	"opus-5.0":    "z-ai/glm-5.3",
	"opus5":       "z-ai/glm-5.3",
	"claude-5":    "z-ai/glm-5.3",
	"opus-4-8":    "z-ai/glm-5.3",
	"opus-4.8":    "z-ai/glm-5.3",
	"claude-4-8":  "z-ai/glm-5.3",
	"claude-4.8":  "z-ai/glm-5.3",
	// Fable family aliases follow GLM-5.3 as well.
	"fable":        "z-ai/glm-5.3",
	"claude-fable": "z-ai/glm-5.3",
	"fable-5.1":    "z-ai/glm-5.3",
	"fable-5-1":    "z-ai/glm-5.3",
	"fable5.1":     "z-ai/glm-5.3",
	"fable51":      "z-ai/glm-5.3",
	"fable-5":      "z-ai/glm-5.3",
	"fable5":       "z-ai/glm-5.3",
	// Sonnet family aliases follow DeepSeek V4 Pro; Sonnet 4.6 follows the
	// V4.1 Flash.
	"sonnet":        "deepseek/deepseek-v4-pro",
	"claude-sonnet": "deepseek/deepseek-v4-pro",
	"sonnet-5.5":    "deepseek/deepseek-v4-pro",
	"sonnet-5-5":    "deepseek/deepseek-v4-pro",
	"sonnet5.5":     "deepseek/deepseek-v4-pro",
	"sonnet55":      "deepseek/deepseek-v4-pro",
	"sonnet-5":      "deepseek/deepseek-v4-pro",
	"sonnet-4-6":    "deepseek/deepseek-v4.1-flash",
	"sonnet-4.6":    "deepseek/deepseek-v4.1-flash",
	"haiku":         "z-ai/glm-5.3-flash",
	"claude-haiku":  "z-ai/glm-5.3-flash",
	"haiku-5.5":     "z-ai/glm-5.3-flash",
	"haiku-5-5":     "z-ai/glm-5.3-flash",
	"haiku5.5":      "z-ai/glm-5.3-flash",
	"haiku55":       "z-ai/glm-5.3-flash",
	"haiku-4-5":     "z-ai/glm-5.3-flash",
	"haiku-4.5":     "z-ai/glm-5.3-flash",
	// The GPT family follows the one OpenAI-named row the upstream publishes.
	"gpt":          "openai/gpt-oss-120b",
	"openai":       "openai/gpt-oss-120b",
	"gpt-oss-120b": "openai/gpt-oss-120b",
	// Google family follows Gemma 4 31B.
	"google":      "google/gemma-4-31b",
	"gemma":       "google/gemma-4-31b",
	"gemma-4":     "google/gemma-4-31b",
	"gemma-4-31b": "google/gemma-4-31b",
	// DeepSeek family follows V4.1 Flash; explicit version pins stay exact.
	"deepseek":            "deepseek/deepseek-v4.1-flash",
	"deepseek-pro":        "deepseek/deepseek-v4-pro-0813",
	"deepseek-flash":      "deepseek/deepseek-v4.1-flash",
	"deepseek-v4-1-flash": "deepseek/deepseek-v4.1-flash",
	"deepseek-v4p1-flash": "deepseek/deepseek-v4.1-flash",
	// Qwen family follows 3.8 27B.
	"qwen":              "qwen/qwen3.8-27b",
	"qwen3.8-27b":       "qwen/qwen3.8-27b",
	"qwen-3.8-27b":      "qwen/qwen3.8-27b",
	"qwen/qwen-3.8-27b": "qwen/qwen3.8-27b",
	"qwen3.6":           "qwen/qwen3.6-27b",
	"qwen3.6-27b":       "qwen/qwen3.6-27b",
	// Motif family.
	"motif":   "motif/motif-3",
	"motif-3": "motif/motif-3",
	// Kimi aliases follow K3.
	"kimi":    "moonshotai/kimi-k3",
	"kimi-k3": "moonshotai/kimi-k3",
	// GLM aliases follow 5.3; version-specific pins stay exact.
	"glm":           "z-ai/glm-5.3",
	"zai":           "z-ai/glm-5.3",
	"z-ai":          "z-ai/glm-5.3",
	"glm-5.3-flash": "z-ai/glm-5.3-flash",
	"glm-5.3":       "z-ai/glm-5.3",
	// Muse Spark Open spellings (upstream #2075) all resolve to the
	// fork's single Muse Spark catalog row.
	"muse-spark-open": catalog.ModelIDMuseSpark.String(),
	"musespark-open":  catalog.ModelIDMuseSpark.String(),
	"spark-open":      catalog.ModelIDMuseSpark.String(),
})

func unversionedAliasesFollowLatestFamilyRelease(aliases map[string]string) map[string]string {
	servable := func(id string) bool {
		_, ok := servableForceModel(id, "")
		return ok
	}
	for alias, target := range aliases {
		if strings.ContainsAny(alias, "0123456789") {
			continue
		}
		if latest := catalog.LatestInFamily(target, servable); latest != "" {
			aliases[alias] = latest
		}
	}
	return aliases
}
