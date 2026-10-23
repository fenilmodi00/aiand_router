package translate_test

import (
	"net/http"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAnthropicFinalAssistantResponsesProjection(t *testing.T) {
	body := []byte(`{"model":"deepseek/deepseek-v4-pro","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Say hello"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello\n\n"}]}],"stream":true}`)
	conversion, err := translate.ConvertResponsesToChatCompletionsWithOptions(body, translate.ResponsesConversionOptions{PortableCodex: true})
	require.NoError(t, err)
	envelope, err := translate.ParseOpenAI(conversion.Body)
	require.NoError(t, err)
	prepared, err := envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.2", Capabilities: router.Lookup("z-ai/glm-5.2"), TargetProvider: providers.ProviderAIAND})
	require.NoError(t, err)
	assert.Equal(t, "Hello", gjson.GetBytes(prepared.Body, "messages.1.content.0.text").String())
}

func TestAnthropicFinalAssistantNativeContent(t *testing.T) {
	cases := []struct {
		name, content string
		wantText      string
		wantCount     int
	}{
		{"string", `"  Hello\n \t"`, "  Hello", 1},
		{"text block", `[{"type":"text","text":"  Hello\n \t"}]`, "  Hello", 1},
		{"unicode whitespace", `"Hello\u00a0\u2003"`, "Hello", 1},
		{"trailing empty blocks", `[{"type":"text","text":"Hello\n"},{"type":"text","text":" \t\n"},{"type":"text","text":""}]`, "Hello", 1},
		{"earlier text block preserved", `[{"type":"text","text":"First\n\n"},{"type":"text","text":"Last\n\n"}]`, "Last", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"Question"},{"role":"assistant","content":` + tc.content + `}]}`)
			prepared := prepareFinalAssistantAnthropic(t, body)
			blocks := gjson.GetBytes(prepared, "messages.1.content").Array()
			require.Len(t, blocks, tc.wantCount)
			assert.Equal(t, tc.wantText, blocks[len(blocks)-1].Get("text").String())
			if tc.wantCount == 2 {
				assert.Equal(t, "First\n\n", blocks[0].Get("text").String())
			}
		})
	}
}

func TestAnthropicFinalAssistantWhitespaceOnlyRemoved(t *testing.T) {
	for _, content := range []string{`" \t\n"`, `[{"type":"text","text":" \t\n"},{"type":"text","text":""}]`} {
		t.Run(content, func(t *testing.T) {
			prepared := prepareFinalAssistantAnthropic(t, []byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"Question\n"},{"role":"assistant","content":`+content+`}]}`))
			messages := gjson.GetBytes(prepared, "messages").Array()
			require.Len(t, messages, 1)
			assert.Equal(t, "user", messages[0].Get("role").String())
			assert.Equal(t, "Question\n", messages[0].Get("content.0.text").String())
		})
	}
}

func TestAnthropicFinalAssistantPreservesEarlierTurns(t *testing.T) {
	prepared := prepareFinalAssistantAnthropic(t, []byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"Question\n\n"},{"role":"assistant","content":"Earlier\n\n"},{"role":"user","content":"Follow up\n\n"},{"role":"assistant","content":"Final\n\n"}]}`))
	assert.Equal(t, "Question\n\n", gjson.GetBytes(prepared, "messages.0.content").String())
	assert.Equal(t, "Earlier\n\n", gjson.GetBytes(prepared, "messages.1.content").String())
	assert.Equal(t, "Follow up\n\n", gjson.GetBytes(prepared, "messages.2.content").String())
	assert.Equal(t, "Final", gjson.GetBytes(prepared, "messages.3.content.0.text").String())
}

func TestAnthropicFinalAssistantConsecutiveEmptyMessages(t *testing.T) {
	prepared := prepareFinalAssistantAnthropic(t, []byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"Question"},{"role":"assistant","content":"Answer\n\n"},{"role":"assistant","content":" \n"},{"role":"assistant","content":[{"type":"text","text":"\t"}]}]}`))
	require.Len(t, gjson.GetBytes(prepared, "messages").Array(), 2)
	assert.Equal(t, "Answer", gjson.GetBytes(prepared, "messages.1.content.0.text").String())
}

func TestAnthropicFinalAssistantUserEndingHistoryUnchanged(t *testing.T) {
	prepared := prepareFinalAssistantAnthropic(t, []byte(`{"model":"z-ai/glm-5.2","messages":[{"role":"user","content":"Question"},{"role":"assistant","content":"Answer\n\n"},{"role":"user","content":"Next\n\n"}]}`))
	require.Len(t, gjson.GetBytes(prepared, "messages").Array(), 3)
	assert.Equal(t, "Answer\n\n", gjson.GetBytes(prepared, "messages.1.content").String())
	assert.Equal(t, "Next\n\n", gjson.GetBytes(prepared, "messages.2.content.0.text").String())
}

func prepareFinalAssistantAnthropic(t *testing.T, body []byte) []byte {
	t.Helper()
	envelope, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	prepared, err := envelope.PrepareAnthropic(http.Header{}, translate.EmitOptions{TargetModel: "z-ai/glm-5.2", Capabilities: router.Lookup("z-ai/glm-5.2"), TargetProvider: providers.ProviderAIAND})
	require.NoError(t, err)
	return prepared.Body
}
