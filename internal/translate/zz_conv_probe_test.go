package translate_test

import (
	"testing"

	"aiand-os/router/internal/translate"
)

func TestProbeStringInputWithMaxTokens(t *testing.T) {
	conv, err := translate.ConvertResponsesToChatCompletionsWithOptions(
		[]byte(`{"model":"gpt-4o-mini","input":"Say hello","max_output_tokens":16}`),
		translate.ResponsesConversionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("body: %s", conv.Body)
	conv2, err := translate.ConvertResponsesToChatCompletionsWithOptions(
		[]byte(`{"model":"gpt-4o-mini","input":[{"role":"user","content":[{"type":"input_text","text":"Say hello"}]}],"max_output_tokens":16}`),
		translate.ResponsesConversionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("body2: %s", conv2.Body)
}
