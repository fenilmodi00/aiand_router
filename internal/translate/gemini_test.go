package translate_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"aiand-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----- helpers -----

func mustUnmarshal(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

func must(t *testing.T, m map[string]any, k string) any {
	t.Helper()
	v, ok := m[k]
	require.True(t, ok, "missing key %s", k)
	return v
}

func TestIsIntrinsicallyIncompatible(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "wrapped gemini schema incompatible",
			err:  fmt.Errorf("prepare gemini: %w", translate.ErrGeminiSchemaIncompatible),
			want: true,
		},
		{
			name: "wrapped reasoning incompatible",
			err:  fmt.Errorf("prepare gemini: %w", translate.ErrReasoningIncompatible),
			want: true,
		},
		{
			name: "wrapped gemini unsigned tool history",
			err:  fmt.Errorf("prepare gemini: %w", translate.ErrGeminiUnsignedToolHistory),
			want: true,
		},
		{
			name: "unrelated error",
			err:  errors.New("other"),
			want: false,
		},
		{
			name: "tool declaration conflict is a client bug, not intrinsic incompatibility",
			err:  translate.ErrGeminiToolDeclarationConflict,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, translate.IsIntrinsicallyIncompatible(tt.err))
		})
	}
}
