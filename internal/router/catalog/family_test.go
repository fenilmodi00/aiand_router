package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLatestInFamily(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		eligible []string
		want     string
	}{
		{"GLM keeps variant", "z-ai/glm-5.2", []string{"z-ai/glm-5.2", "z-ai/glm-5.3", "z-ai/glm-5.3-flash"}, "z-ai/glm-5.3"},
		{"GLM flash is its own family", "z-ai/glm-5.3-flash", []string{"z-ai/glm-5.2", "z-ai/glm-5.3", "z-ai/glm-5.3-flash"}, "z-ai/glm-5.3-flash"},
		{"Kimi major", "moonshotai/kimi-k2.7", []string{"moonshotai/kimi-k2.7", "moonshotai/kimi-k3"}, "moonshotai/kimi-k3"},
		{"dated alias is its own family", "deepseek/deepseek-v4-pro", []string{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-pro-0813"}, "deepseek/deepseek-v4-pro"},
		{"newest unavailable", "z-ai/glm-5.2", []string{"z-ai/glm-5.2", "z-ai/glm-5.3"}, "z-ai/glm-5.3"},
		{"no downgrade", "z-ai/glm-5.3", []string{"z-ai/glm-5.2"}, ""},
		{"unknown ID", "unknown-1", []string{"unknown-1"}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			eligible := make(map[string]struct{}, len(test.eligible))
			for _, model := range test.eligible {
				eligible[model] = struct{}{}
			}
			assert.Equal(t, test.want, LatestInFamily(test.model, func(model string) bool {
				_, allowed := eligible[model]
				return allowed
			}))
		})
	}
}

func TestFamilyAndVersion(t *testing.T) {
	tests := []struct {
		id      string
		family  string
		version [2]int
		ok      bool
	}{
		{"z-ai/glm-5.2", "z-ai/glm", [2]int{5, 2}, true},
		{"z-ai/glm-5.3", "z-ai/glm", [2]int{5, 3}, true},
		{"z-ai/glm-5.3-flash", "z-ai/glm-flash", [2]int{5, 3}, true},
		{"moonshotai/kimi-k2.7", "moonshotai/kimi-k", [2]int{2, 7}, true},
		{"moonshotai/kimi-k3", "moonshotai/kimi-k", [2]int{3, 0}, true},
		{"deepseek/deepseek-v4-pro", "deepseek/deepseek-v-pro", [2]int{4, 0}, true},
		{"deepseek/deepseek-v4-flash", "deepseek/deepseek-v-flash", [2]int{4, 0}, true},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			family, version, ok := FamilyAndVersion(tt.id)
			if ok != tt.ok {
				t.Fatalf("FamilyAndVersion(%q) ok = %v, want %v", tt.id, ok, tt.ok)
			}
			if !ok {
				return
			}
			if family != tt.family {
				t.Errorf("FamilyAndVersion(%q) family = %q, want %q", tt.id, family, tt.family)
			}
			if version != tt.version {
				t.Errorf("FamilyAndVersion(%q) version = %v, want %v", tt.id, version, tt.version)
			}
		})
	}
}

func TestFamilyDuplicates(t *testing.T) {
	ids := []string{
		"z-ai/glm-5.2",
		"z-ai/glm-5.3",
		"moonshotai/kimi-k2.7",
		"moonshotai/kimi-k3",
	}
	dups := FamilyDuplicates(ids)
	got := make(map[string]string, len(dups))
	for _, d := range dups {
		got[d.Superseded] = d.SupersededBy
	}
	want := map[string]string{
		"z-ai/glm-5.2":        "z-ai/glm-5.3",
		"moonshotai/kimi-k2.7": "moonshotai/kimi-k3",
	}
	if len(got) != len(want) {
		t.Fatalf("FamilyDuplicates(%v) = %v, want %v", ids, dups, want)
	}
	for supersededID, wantBy := range want {
		gotBy, ok := got[supersededID]
		if !ok {
			t.Errorf("expected %q to be flagged as superseded, was not", supersededID)
			continue
		}
		if gotBy != wantBy {
			t.Errorf("FamilyDuplicates: %q superseded by %q, want %q", supersededID, gotBy, wantBy)
		}
	}
}

func TestFamilyDuplicates_NoFalsePositiveOnDistinctSizes(t *testing.T) {
	ids := []string{
		"deepseek/deepseek-v4-flash",
		"deepseek/deepseek-v4-pro",
		"z-ai/glm-5.3-flash",
	}
	if dups := FamilyDuplicates(ids); len(dups) != 0 {
		t.Errorf("FamilyDuplicates(%v) = %v, want no duplicates", ids, dups)
	}
}
