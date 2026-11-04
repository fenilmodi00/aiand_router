package proxy

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"testing"

	"aiand-os/router/internal/providers"
	"aiand-os/router/internal/router"
	"aiand-os/router/internal/router/turntype"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Claude Code trims the oldest rounds and retries a compaction only when the
// error detail matches this pattern; it reads the token gap from the numbers.
var claudeCodePromptTooLong = regexp.MustCompile(`(?i)^prompt is too long[^0-9]*(\d+)\s*tokens?\s*>\s*(\d+)`)

// Ported from upstream 0bb731f6 (#1876). The upstream end-to-end gateway
// fixtures were dropped: v0 has no gateway providers or Anthropic upstreams.
func TestCompactionUnservableCarriesTrimCountsForClaudeCode(t *testing.T) {
	withCounts := &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 2100056 tokens > 1000000 maximum"}}`),
	}
	countless := &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long"}}`),
	}
	decision := router.Decision{Provider: providers.ProviderAIAND, Model: "z-ai/glm-5.3"}

	for name, tc := range map[string]struct {
		clientApp  string
		primaryErr error
		cause      error
		wantCounts bool
	}{
		"claude_code_with_counts":     {clientApp: ClientAppClaudeCode, primaryErr: withCounts, cause: countless, wantCounts: true},
		"claude_code_counts_on_cause": {clientApp: ClientAppClaudeCode, primaryErr: countless, cause: withCounts, wantCounts: true},
		"claude_code_without_counts":  {clientApp: ClientAppClaudeCode, primaryErr: countless, cause: countless},
		"other_client_with_counts":    {clientApp: ClientAppOpencode, primaryErr: withCounts, cause: withCounts},
	} {
		t.Run(name, func(t *testing.T) {
			overflow := asCompactionUnservable(turntype.Compaction, decision, tc.cause)
			require.NotNil(t, overflow)
			err := withClientTrimCounts(context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: tc.clientApp}), overflow, tc.primaryErr)
			class, ok := ClassifyDispatchError(err)
			require.True(t, ok)
			if !tc.wantCounts {
				assert.Equal(t, DispatchErrorCompactionUnservable, class.Kind)
				assert.Equal(t, http.StatusBadGateway, class.Status)
				assert.True(t, class.NoClientRetry)
				assert.NotContains(t, class.Message, "prompt is too long")
				return
			}
			assert.Equal(t, http.StatusBadRequest, class.Status)
			m := claudeCodePromptTooLong.FindStringSubmatch(class.Message)
			require.NotNil(t, m, "client-visible message %q", class.Message)
			assert.Equal(t, "2100056", m[1])
			assert.Equal(t, "1000000", m[2])
			assert.NotContains(t, class.Message, "Compact the conversation")
		})
	}
}

func TestPromptTooLongCounts(t *testing.T) {
	_, _, ok := promptTooLongCounts(errors.New("not upstream"))
	assert.False(t, ok)
	tokens, limit, ok := promptTooLongCounts(&providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"error":{"message":"prompt is too long: 1,100 tokens > 1,000 tokens"}}`),
	})
	_ = tokens
	_ = limit
	assert.False(t, ok, "commas are not part of the recognized format")
	tokens, limit, ok = promptTooLongCounts(&providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"error":{"message":"prompt is too long: 1100 tokens \u003e 1000 tokens"}}`),
	})
	assert.True(t, ok)
	assert.Equal(t, 1100, tokens)
	assert.Equal(t, 1000, limit)
}

func TestNonCompactionOverflowPassesThrough(t *testing.T) {
	err := errors.New("ordinary failure")
	assert.Same(t, err, asCompactionUnservable(turntype.Compaction, router.Decision{}, err))
}
