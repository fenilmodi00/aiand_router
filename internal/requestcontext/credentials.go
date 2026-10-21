// Package requestcontext carries the request-scoped, provider-neutral values a
// provider adapter needs to shape one upstream call: resolved credentials,
// effective base URL and model alias, caller identity, and forwarded
// correlation headers. It is inner-ring and I/O-free so adapters can depend on
// it without importing the dispatch orchestrator.
package requestcontext

import (
	"context"
	"net/http"
	"strings"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SubscriptionTokenPrefix marks a Claude subscription (Claude.ai OAuth)
// bearer. Matches the "oat" stem to cover both sk-ant-oat- and the
// real-world sk-ant-oat01-… shapes.
const SubscriptionTokenPrefix = "sk-ant-oat"

// ChatGPTAccountIDHeader is the header Codex sends alongside a ChatGPT
// subscription bearer; its presence disambiguates that JWT from a plain
// OpenAI API key.
const ChatGPTAccountIDHeader = "ChatGPT-Account-ID"

// Credential sources, for logging and precedence reasoning. Never log the key
// itself — only the source.
const (
	SourceBYOK                = "byok"
	SourceClient              = "client"
	SourceSubscription        = "subscription"
	SourceSubscriptionOverage = "subscription_overage"
	SourceCodexSubscription   = "codex_subscription"
)

// Credentials holds the API key to use for an upstream request.
type Credentials struct {
	SubscriptionAccountID string
	APIKey                []byte // never logged
	Source                string // SourceBYOK | SourceClient | SourceSubscription | SourceCodexSubscription
	// OAuth marks a subscription bearer (Claude sk-ant-oat- token, Anthropic
	// only; or Codex ChatGPT JWT, OpenAI only). Authenticates via
	// Authorization: Bearer, never x-api-key.
	OAuth bool
	// AccountID is the ChatGPT-Account-ID paired with a Codex subscription
	// bearer; the Codex backend 401/403s without it. Never logged.
	AccountID []byte
	// BaseURL overrides the upstream endpoint per-request; non-empty only on BYOK
	// credentials, where the boot-time deployment URL is provider-wide, not per-key.
	BaseURL string
	// ModelAliases rewrites the outbound model ID for endpoints publishing the
	// catalog's models under their own names; non-empty only on BYOK credentials.
	ModelAliases map[string]string
	// IdentityHeader and IdentityHeaderFormat name and shape the header this
	// endpoint wants the caller's identity in; empty forwards nothing.
	IdentityHeader       string
	IdentityHeaderFormat string
	// ForwardedClientHeaders and BaggageHeader configure client-header passthrough; empty forwards nothing.
	ForwardedClientHeaders []string
	BaggageHeader          string
	// AuthType is the BYOK key's auth mode (see auth.AuthType*). APIKey already holds
	// the derived credential; this tells the adapter how the upstream must read it.
	AuthType string
	// PrincipalID names the upstream account this credential authenticates as,
	// independent of the bytes in APIKey: a workload-identity or keypair-JWT
	// credential mints a fresh short-lived bearer per request, so the key is no
	// identity at all. Non-secret and safe to fingerprint; empty when the
	// credential is only known by its key material.
	PrincipalID string
}

// UpstreamPrincipal identifies the account this credential authenticates as,
// preferring the stable PrincipalID and falling back to the key material for
// a static credential that has no other identity. The second return reports
// whether the value is secret and must be hashed before it travels.
func (c *Credentials) UpstreamPrincipal() (principal string, secret bool) {
	if c == nil {
		return "", false
	}
	if c.PrincipalID != "" {
		return c.PrincipalID, false
	}
	return string(c.APIKey), true
}

// CredentialsContextKey is the request-context key for resolved per-request credentials.
type CredentialsContextKey struct{}

// CredentialsFromContext returns the resolved credentials stashed on ctx.
func CredentialsFromContext(ctx context.Context) *Credentials {
	v := ctx.Value(CredentialsContextKey{})
	if v == nil {
		return nil
	}
	creds, _ := v.(*Credentials)
	return creds
}

// WithCredentials stashes creds on ctx. A nil creds is an explicit "none" so
// CredentialsFromContext reports nil and the adapter uses its deployment key.
func WithCredentials(ctx context.Context, creds *Credentials) context.Context {
	return context.WithValue(ctx, CredentialsContextKey{}, creds)
}

// EffectiveBaseURL returns the BYOK key's per-request base URL if set,
// or fallback (the provider's boot-time deployment URL).
func EffectiveBaseURL(ctx context.Context, fallback string) string {
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.BaseURL == "" {
		return fallback
	}
	return strings.TrimRight(creds.BaseURL, "/")
}

// EffectiveUpstreamModel returns the aliased model name for this request's endpoint, or model
// unchanged. Only the outbound wire name changes -- routing, pricing, and telemetry use the catalog ID.
func EffectiveUpstreamModel(ctx context.Context, model string) string {
	creds := CredentialsFromContext(ctx)
	if creds == nil {
		return model
	}
	if alias, ok := creds.ModelAliases[model]; ok {
		return alias
	}
	return model
}

// ApplyModelAlias rewrites the body's top-level "model" field via the BYOK credential alias.
// Unaliased bodies are returned untouched; the envelope stays the authority on every other request.
func ApplyModelAlias(ctx context.Context, body []byte, model string) []byte {
	creds := CredentialsFromContext(ctx)
	if creds == nil || len(body) == 0 {
		return body
	}
	// Presence, not inequality: an alias equal to the catalog id still has to
	// overwrite a catalog UpstreamID an adapter already wrote into the body.
	upstreamModel, aliased := creds.ModelAliases[model]
	if !aliased {
		return body
	}
	if !gjson.GetBytes(body, "model").Exists() {
		return body
	}
	out, err := sjson.SetBytes(body, "model", upstreamModel)
	if err != nil {
		return body
	}
	return out
}

// ExtractClientCredentials extracts provider credentials from request headers.
// The OpenAI-compatible upstream authenticates via Authorization: Bearer.
//
// Rejects any token with auth.APIKeyPrefix — router-issued bearers (rk_...)
// use the same headers via WithAuth, and this stops them leaking upstream.
func ExtractClientCredentials(provider string, headers http.Header) *Credentials {
	if providers.FamilyFor(provider) != providers.FamilyOpenAICompat {
		return nil
	}
	authHeader := headers.Get("Authorization")
	if raw, found := strings.CutPrefix(authHeader, "Bearer "); found {
		key := strings.TrimSpace(raw)
		// Reject Anthropic-shaped tokens (keys and OAuth bearers) so one
		// Bearer header isn't misidentified as creds for every provider.
		if key != "" && !auth.HasAPIKeyPrefix(key) && !strings.HasPrefix(key, "sk-ant-") {
			return &Credentials{APIKey: []byte(key), Source: SourceClient}
		}
	}
	return nil
}

// SubscriptionCredsFromToken returns subscription credentials for a bare
// token, or nil if it isn't a Claude subscription bearer. Anthropic-only.
func SubscriptionCredsFromToken(token string) *Credentials {
	if !strings.HasPrefix(token, SubscriptionTokenPrefix) {
		return nil
	}
	return &Credentials{APIKey: []byte(token), Source: SourceSubscription, OAuth: true}
}

// CodexSubscriptionCreds returns Codex subscription credentials for a
// ChatGPT-login JWT paired with its account id, or nil otherwise. Rejects
// router keys and OpenAI API keys; requires a non-empty account id since
// the Codex backend 401/403s without it.
func CodexSubscriptionCreds(token, accountID string) *Credentials {
	token = strings.TrimSpace(token)
	accountID = strings.TrimSpace(accountID)
	if token == "" || accountID == "" {
		return nil
	}
	if auth.HasAPIKeyPrefix(token) || strings.HasPrefix(token, "sk-") {
		return nil
	}
	// The bearer is a refreshed OAuth JWT; the account id is what stays put
	// across a refresh, so it is this credential's principal.
	return &Credentials{
		APIKey:      []byte(token),
		AccountID:   []byte(accountID),
		Source:      SourceCodexSubscription,
		OAuth:       true,
		PrincipalID: "chatgpt-account:" + accountID,
	}
}

// CodexCoveredModels returns a copy of the models a Codex subscription may serve.
func CodexCoveredModels() []string {
	return catalog.CodexSubscriptionModels()
}

// CodexSubscriptionCanAttemptModel reports whether model belongs to the native
// Codex roster or is explicitly enabled for a subscription-first funding
// attempt in the catalog. Unknown models fail closed.
func CodexSubscriptionCanAttemptModel(model string) bool {
	if CodexSubscriptionCoversModel(model) {
		return true
	}
	m, ok := catalog.ByID(model)
	if !ok || !m.CodexSubscriptionFallback {
		return false
	}
	for _, binding := range m.Providers {
		if binding.Provider == providers.ProviderAIAND {
			return true
		}
	}
	return false
}

// CodexSubscriptionCoversModel reports whether model belongs to the curated
// automatic Codex model roster. This is narrower than
// CodexSubscriptionCanAttemptModel, which also permits explicitly approved
// catalog fallback targets outside the automatic roster.
func CodexSubscriptionCoversModel(model string) bool {
	return catalog.CodexSubscriptionCoversModel(model)
}

// ApplyWIFTokenType marks the bearer as a workload attestation, not an upstream-issued token.
// Must be called after prep.Headers are copied so a client-supplied value can't suppress it.
func ApplyWIFTokenType(ctx context.Context, upstream *http.Request) {
	creds := CredentialsFromContext(ctx)
	if creds == nil || creds.AuthType != auth.AuthTypeWIF {
		return
	}
	upstream.Header.Set(auth.WIFTokenTypeHeader, auth.WIFTokenTypeValue)
}
