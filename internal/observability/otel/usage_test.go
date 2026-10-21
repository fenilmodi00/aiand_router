package otel_test

import (
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/observability/otel"
	"weave-os/router/internal/providers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageExtractor_OpenAIResponsesNonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, providers.ProviderAIAND)

	body := `{"id":"resp_2","object":"response","usage":{"input_tokens":50,"output_tokens":9,"input_tokens_details":{"cached_tokens":0}}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 50, in)
	assert.Equal(t, 9, out)
}

func TestUsageExtractor_GoogleStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, providers.ProviderAIAND)

	events := []string{
		"data: {\"id\":\"chatcmpl-g\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"Yo\"}}]}\n\n",
		"data: {\"id\":\"chatcmpl-g\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":5}}\n\n",
		"data: [DONE]\n\n",
	}

	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 30, in)
	assert.Equal(t, 5, out)
}

func TestUsageExtractor_NoUsageReturnsZero(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, providers.ProviderAIAND)

	body := `{"id":"msg_1","type":"message","content":[]}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 0, in)
	assert.Equal(t, 0, out)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 0, cacheCreation)
	assert.Equal(t, 0, cacheRead)
}

func TestUsageExtractor_OpenAICacheTokens_NonStreaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, providers.ProviderAIAND)

	// OpenAI exposes cached prompt tokens via prompt_tokens_details.cached_tokens.
	// GPT-5.4 and earlier have no write field, so cache_creation stays at 0.
	body := `{"id":"chatcmpl-2","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":15,"completion_tokens":8,"total_tokens":23,"prompt_tokens_details":{"cached_tokens":42}}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	in, out := ext.Tokens()
	assert.Equal(t, 15, in)
	assert.Equal(t, 8, out)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 0, cacheCreation)
	assert.Equal(t, 42, cacheRead)
}

func TestUsageExtractor_OpenAIChatCompletionsCacheWriteTokens(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, providers.ProviderAIAND)

	body := `{"id":"chatcmpl-2","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":80,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":42,"cache_write_tokens":16}}}`
	_, err := ext.Write([]byte(body))
	require.NoError(t, err)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 16, cacheCreation)
	assert.Equal(t, 42, cacheRead)
}

func TestUsageExtractor_OpenAICacheTokens_Streaming(t *testing.T) {
	rec := httptest.NewRecorder()
	ext := otel.NewUsageExtractor(rec, providers.ProviderAIAND)

	events := []string{
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n",
		"data: {\"id\":\"chatcmpl-2\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":12,\"prompt_tokens_details\":{\"cached_tokens\":7}}}\n\n",
		"data: [DONE]\n\n",
	}

	for _, e := range events {
		_, err := ext.Write([]byte(e))
		require.NoError(t, err)
	}

	in, out := ext.Tokens()
	assert.Equal(t, 20, in)
	assert.Equal(t, 12, out)

	cacheCreation, cacheRead := ext.CacheTokens()
	assert.Equal(t, 0, cacheCreation)
	assert.Equal(t, 7, cacheRead)
}

func TestUsageExtractor_RecordCacheUsage_NilReceiver(t *testing.T) {
	var ext *otel.UsageExtractor
	creation, read := ext.CacheTokens()
	assert.Equal(t, 0, creation)
	assert.Equal(t, 0, read)
}
