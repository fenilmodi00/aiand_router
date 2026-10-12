package translate

import (
	"bytes"

	"weave-os/router/internal/router"

	"github.com/tidwall/gjson"
)

// TranslationRequirements extracts the semantic contract carried by an
// already-parsed request. It does not decide eligibility; proxy combines this
// value with configured provider families and catalog capabilities.
func (e *RequestEnvelope) TranslationRequirements(endpoint router.TranslationEndpoint) router.TranslationRequirements {
	req := router.TranslationRequirements{
		Endpoint:      endpoint,
		FunctionTools: e.HasTools(),
		Images:        e.HasImages(),
	}
	switch e.format {
	case FormatAnthropic:
		req.SourceFormat = router.WireFormatAnthropic
		req.ReasoningReplay = gjson.GetBytes(e.body, "thinking").Exists() || hasContentType(e.body, "thinking")
		req.ReasoningSignature = gjson.GetBytes(e.body, "messages.#(content.#.signature!=\"\")").Exists()
		req.PromptCacheControl = containsKey(e.body, "cache_control")
		req.StructuredOutput = gjson.GetBytes(e.body, "output_config.format").Exists()
		req.Audio, req.Files = anthropicMediaRequirements(e.body)
		req.CitationsOrSearch = len(e.NativeServerTools()) > 0
	case FormatOpenAI:
		req.SourceFormat = router.WireFormatOpenAI
		req.ReasoningReplay = hasContentType(e.body, "reasoning") || gjson.GetBytes(e.body, "reasoning").Exists()
		req.StructuredOutput = gjson.GetBytes(e.body, "response_format").Exists()
		req.UsageDetail = gjson.GetBytes(e.body, "stream_options.include_usage").Bool()
		req.Audio, req.Files = openAIMediaRequirements(e.body)
		req.CitationsOrSearch = len(e.NativeServerTools()) > 0
	}
	return req
}

func hasContentType(body []byte, typ string) bool {
	return gjson.GetBytes(body, "messages.#(content.#.type==\""+typ+"\")").Exists() ||
		gjson.GetBytes(body, "input.#(type==\""+typ+"\")").Exists()
}

func containsKey(body []byte, key string) bool {
	return bytes.Contains(body, []byte(`"`+key+`"`))
}

func containsAnyKey(body []byte, keys ...string) bool {
	for _, key := range keys {
		if containsKey(body, key) {
			return true
		}
	}
	return false
}

// anthropicMediaRequirements identifies non-image content blocks; files and audio
// need separate constraints because text translation can silently discard them.
func anthropicMediaRequirements(body []byte) (audio, files bool) {
	gjson.GetBytes(body, "messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "audio", "input_audio":
				audio = true
			case "document", "file", "input_file", "container_upload":
				files = true
			}
			return !audio || !files
		})
		return !audio || !files
	})
	return audio, files
}

// openAIMediaRequirements covers both Chat content parts and the Responses
// input union. The latter is scanned recursively because Responses message
// items nest their content under input[].content rather than messages[].
func openAIMediaRequirements(body []byte) (audio, files bool) {
	visitOpenAIMedia(gjson.ParseBytes(body), &audio, &files)
	return audio, files
}

func visitOpenAIMedia(value gjson.Result, audio, files *bool) {
	if value.IsObject() {
		typ := value.Get("type").String()
		switch typ {
		case "input_audio", "audio", "audio_url":
			*audio = true
		case "input_file", "file", "file_url":
			*files = true
		}
		value.ForEach(func(_, nested gjson.Result) bool {
			visitOpenAIMedia(nested, audio, files)
			return !*audio || !*files
		})
		return
	}
	if value.IsArray() {
		value.ForEach(func(_, nested gjson.Result) bool {
			visitOpenAIMedia(nested, audio, files)
			return !*audio || !*files
		})
	}
}
