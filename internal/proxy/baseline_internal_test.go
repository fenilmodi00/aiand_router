package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBaselineFor(t *testing.T) {
	t.Run("known model returns itself", func(t *testing.T) {
		s := &Service{defaultBaselineModel: "deepseek/deepseek-v4.1-flash"}
		assert.Equal(t, "z-ai/glm-5.2", s.baselineFor("z-ai/glm-5.2"))
	})

	t.Run("unknown model returns baseline", func(t *testing.T) {
		s := &Service{defaultBaselineModel: "deepseek/deepseek-v4.1-flash"}
		assert.Equal(t, "deepseek/deepseek-v4.1-flash", s.baselineFor("aiand-router"))
	})

	t.Run("empty model returns baseline", func(t *testing.T) {
		s := &Service{defaultBaselineModel: "deepseek/deepseek-v4.1-flash"}
		assert.Equal(t, "deepseek/deepseek-v4.1-flash", s.baselineFor(""))
	})

	t.Run("unknown model with no baseline returns empty", func(t *testing.T) {
		s := &Service{}
		assert.Equal(t, "", s.baselineFor("aiand-router"))
	})
}

func TestWithDefaultBaselineModel(t *testing.T) {
	s := &Service{}
	s.WithDefaultBaselineModel("deepseek/deepseek-v4.1-flash")
	assert.Equal(t, "deepseek/deepseek-v4.1-flash", s.defaultBaselineModel)
}
