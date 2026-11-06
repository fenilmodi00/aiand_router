package main

import (
	"os"
	"testing"
)

func TestStoreSaveMakesCassetteReadableAfterContainerExit(t *testing.T) {
	s, err := newStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const key = "cassette-key"
	if err := s.save(key, &cassette{
		Method:     "POST",
		Path:       "/v1/messages",
		StatusCode: 200,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       rawTextBody(`{"ok":true}`),
	}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(s.path(key))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("cassette mode = %o, want 644", got)
	}
	if _, err := os.ReadFile(s.path(key)); err != nil {
		t.Fatalf("saved cassette is not readable: %v", err)
	}
}

// TestRequestKeyIgnoresPromptCacheKey pins that per-run session-affinity hints
// don't change the cassette key, so replay-only CI stays green.
func TestRequestKeyIgnoresPromptCacheKey(t *testing.T) {
	base := `{"model":"gpt-5.1","input":"hi"}`
	runA := `{"model":"gpt-5.1","input":"hi","prompt_cache_key":"aaaa"}`
	runB := `{"model":"gpt-5.1","input":"hi","prompt_cache_key":"bbbb"}`

	keyA := requestKey("POST", "/v1/responses", []byte(runA), "")
	keyB := requestKey("POST", "/v1/responses", []byte(runB), "")
	if keyA != keyB {
		t.Errorf("prompt_cache_key changed the cassette key: %s != %s", keyA, keyB)
	}
	// Cassettes recorded before the router started sending the hint must keep
	// replaying, so the hinted body has to hash like the unhinted one.
	if want := requestKey("POST", "/v1/responses", []byte(base), ""); keyA != want {
		t.Errorf("hinted body key %s, want unhinted key %s", keyA, want)
	}
}

// TestRequestKeyDistinguishesScenarios guards against normalization collapsing
// genuinely different requests onto one cassette.
func TestRequestKeyDistinguishesScenarios(t *testing.T) {
	one := requestKey("POST", "/v1/responses", []byte(`{"input":"hi"}`), "")
	two := requestKey("POST", "/v1/responses", []byte(`{"input":"bye"}`), "")
	if one == two {
		t.Error("different request bodies hashed to the same cassette key")
	}
	if p := requestKey("POST", "/v1/messages", []byte(`{"input":"hi"}`), ""); p == one {
		t.Error("different paths hashed to the same cassette key")
	}
}

// TestNormalizeRequestBodyLeavesOtherBodiesUntouched keeps the Anthropic
// cassettes valid: a body with no volatile field must hash its exact bytes.
func TestNormalizeRequestBodyLeavesOtherBodiesUntouched(t *testing.T) {
	body := []byte(`{"model":"deepseek/deepseek-v4.1-flash","metadata":{"user_id":"smoke-basic"}}`)
	if got := string(normalizeRequestBody(body)); got != string(body) {
		t.Errorf("body rewritten: %s", got)
	}
	if got := string(normalizeRequestBody([]byte("not json"))); got != "not json" {
		t.Errorf("non-JSON body rewritten: %s", got)
	}
}

// TestRequestKeyIdentityPathsDistinguishCredentials pins that gateway identity
// endpoints key on the caller's Authorization value: two pasted keys must not
// collide on one /api/user cassette (the login scenario's second-account
// check depends on two distinct identities replaying distinct responses).
// The credential value itself is never persisted — it participates only in
// the key hash and is stripped from stored headers.
func TestRequestKeyIdentityPathsDistinguishCredentials(t *testing.T) {
	keyA := requestKey("GET", "/api/user", nil, "Bearer sk-aiand-smoke-login-a")
	keyB := requestKey("GET", "/api/user", nil, "Bearer sk-aiand-smoke-login-b")
	if keyA == keyB {
		t.Error("distinct credentials hashed to the same /api/user cassette key")
	}

	// The orgs listing takes the same treatment.
	orgsA := requestKey("GET", "/api/orgs", nil, "Bearer sk-a")
	orgsB := requestKey("GET", "/api/orgs", nil, "Bearer sk-b")
	if orgsA == orgsB {
		t.Error("distinct credentials hashed to the same /api/orgs cassette key")
	}

	// Provider paths are unaffected: the identity value must not leak into
	// their keys, or every recorded cassette becomes a replay miss.
	providerA := requestKey("POST", "/v1/chat/completions", []byte(`{"model":"m"}`), "Bearer sk-a")
	providerB := requestKey("POST", "/v1/chat/completions", []byte(`{"model":"m"}`), "Bearer sk-b")
	if providerA != providerB {
		t.Error("provider cassette key changed with the Authorization value")
	}

	// Empty identity (no header) must still hash deterministically.
	if emptyA, emptyB := requestKey("GET", "/api/user", nil, ""), requestKey("GET", "/api/user", nil, ""); emptyA != emptyB {
		t.Error("empty identity key is unstable")
	}
}
