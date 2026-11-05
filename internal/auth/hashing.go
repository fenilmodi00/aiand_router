package auth

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	APITokenPrefixLen = 8
	APITokenSuffixLen = 4
)

func HashAPIKeySHA256(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func APITokenFingerprint(token string) (hashHex, prefix, suffix string) {
	hashHex = HashAPIKeySHA256(token)
	if len(token) <= APITokenPrefixLen+APITokenSuffixLen {
		return hashHex, token, ""
	}
	return hashHex, token[:APITokenPrefixLen], token[len(token)-APITokenSuffixLen:]
}

// MaskAiandKey renders the dashboard-safe form of an Aiand key: first 8 and
// last 4 characters separated by an ellipsis. This is the only safe form
// outside memory; full keys are never logged or echoed.
func MaskAiandKey(key string) string {
	_, prefix, suffix := APITokenFingerprint(key)
	if suffix == "" {
		return prefix + "…"
	}
	return prefix + "…" + suffix
}
