package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"aiand-os/router/internal/observability"
)

// LoginBrowserCookieName is the short-lived HttpOnly cookie that carries the
// sealed browser-login flow state between POST /auth/browser/start and POST
// /auth/browser/exchange. It is NOT a session cookie: the value is an
// encrypted blob (verifier + state + redirect binding) and it dies as soon as
// the exchange completes, succeeds or not.
const LoginBrowserCookieName = "router_browser_login"

// loginBrowserTTL bounds one browser login round trip. The start handler sets
// the cookie's Max-Age and the sealed expires_at to the same value, so the
// cookie, the ciphertext, and the gateway's own authorization-code lifetime
// all expire together.
const loginBrowserTTL = 10 * time.Minute

// browserLoginAADLabel is the purpose half of the browser-flow ciphertext's
// AAD. Like the device code's seal, the external-id half is empty: the
// sealed blob does not yet belong to any account.
const browserLoginAADLabel = "aiand-browser-login"

// browserStateBytes / browserVerifierBytes size the CSRF state and the PKCE
// verifier. 16 state bytes and 32 verifier bytes are 128/256 bits of
// entropy — beyond brute force for a one-shot flow.
const (
	browserStateBytes     = 16
	browserVerifierBytes  = 32
	browserNextMaxLen     = 512
	browserNextDefault    = "/dashboard"
	browserChallengeS256  = "S256"
	browserAuthorizeKind  = "browser_start"
	browserExchangeKind   = "browser_exchange"
	browserUnsupportedMsg = "browser_unsupported"
	browserStateErrMsg    = "invalid_state"
	browserRejectedMsg    = "exchange_rejected"
)

// ErrBrowserFlowState marks a browser exchange whose sealed cookie could not
// be trusted: undecryptable, wrong AAD, expired, or a state that does not
// match the callback's. Handlers map it to 400; the cookie is always cleared.
var ErrBrowserFlowState = errors.New("aiand: browser login flow state invalid")

// SealedBrowserLogin is the JSON payload sealed into the browser-login
// cookie. Verifier and state are the PKCE/CSRF pair; RedirectURI binds the
// exchange to the origin the authorize URL was built from; Next is the
// sanitized post-login destination; ExpiresAt is the absolute TTL checked at
// exchange time (the cookie's own Max-Age is advisory). Exported so handler
// tests can inspect the NoOp-encrypted payload.
type SealedBrowserLogin struct {
	Verifier    string    `json:"verifier"`
	State       string    `json:"state"`
	RedirectURI string    `json:"redirect_uri"`
	Next        string    `json:"next"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// LoginBrowserStart is the dashboard-facing half of a browser-login start.
type LoginBrowserStart struct {
	AuthorizeURL string
	ExpiresIn    int
	Sealed       []byte
}

// LoginBrowserResult is a completed browser login: the minted session grant
// plus the display fields the exchange handler returns.
type LoginBrowserResult struct {
	Grant   *LoginSessionGrant
	Email   string
	OrgName string
	Next    string
}

// sanitizeBrowserNext normalizes the caller-supplied post-login destination:
// empty or malformed values fall back to /dashboard, only same-origin
// absolute paths survive (a leading single slash, never // or /\, which
// browsers treat as protocol-relative), and the value stays bounded so a
// hostile Next cannot bloat the sealed cookie. Stale legacy settings paths
// (a tab open across the sidebar-navigation deploy) map to their new
// top-level pages, query preserved; see
// docs/plans/2026-11-07-sidebar-navigation-migration.md.
func sanitizeBrowserNext(next string) string {
	if len(next) > browserNextMaxLen {
		return browserNextDefault
	}
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return browserNextDefault
	}
	path := next
	query := ""
	if i := strings.IndexByte(next, '?'); i >= 0 {
		path, query = next[:i], next[i:]
	}
	if target, ok := legacyBrowserNextPaths[path]; ok {
		return target + query
	}
	return next
}

// legacyBrowserNextPaths mirrors the frontend login page's LEGACY_NEXT map.
var legacyBrowserNextPaths = map[string]string{
	"/settings":           "/keys",
	"/settings/providers": "/account",
	"/settings/models":    "/models",
}

// browserRedirectOrigin validates and normalizes the request Origin header
// into the redirect URI the authorize URL is built from: scheme://host[:port]
// with no path, query, or fragment. Anything else (missing header, non-http
// scheme, embedded path) is rejected — the gateway only accepts loopback
// origins, and a permissive parser would let a crafted Origin steer the
// authorization code to an attacker-controlled redirect.
func browserRedirectOrigin(origin string) (string, error) {
	if origin == "" {
		return "", fmt.Errorf("%w: missing Origin header", ErrBrowserFlowState)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return "", fmt.Errorf("%w: unparseable Origin", ErrBrowserFlowState)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%w: Origin must be a bare scheme://host origin", ErrBrowserFlowState)
	}
	return u.Scheme + "://" + u.Host, nil
}

// StartBrowserLogin opens a browser authorization-code login: derive the
// state/PKCE pair, ask the gateway for the authorize URL, preflight that the
// gateway actually serves browser authorization for it, and seal the flow
// state into the bytes the handler drops into the short-lived cookie.
// ErrAiandBrowserUnsupported propagates so the dashboard falls back to the
// device flow without counting a failure.
func (s *LoginService) StartBrowserLogin(ctx context.Context, origin, next string) (LoginBrowserStart, error) {
	redirectURI, err := browserRedirectOrigin(origin)
	if err != nil {
		return LoginBrowserStart{}, err
	}
	safeNext := sanitizeBrowserNext(next)

	verifierRaw := make([]byte, browserVerifierBytes)
	if _, err := rand.Read(verifierRaw); err != nil {
		return LoginBrowserStart{}, fmt.Errorf("mint pkce verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierRaw)
	verifierSum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(verifierSum[:])

	stateRaw := make([]byte, browserStateBytes)
	if _, err := rand.Read(stateRaw); err != nil {
		return LoginBrowserStart{}, fmt.Errorf("mint flow state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(stateRaw)

	authorizeURL := s.gateway.AuthorizeURL(AiandAuthorizeParams{
		RedirectURI:   redirectURI,
		State:         state,
		CodeChallenge: challenge,
		KeyName:       "aiand-router@" + s.hostname,
	})
	if err := s.gateway.BrowserAuthorizeAvailable(ctx, authorizeURL); err != nil {
		if errors.Is(err, ErrAiandBrowserUnsupported) {
			return LoginBrowserStart{}, ErrAiandBrowserUnsupported
		}
		return LoginBrowserStart{}, aiandGatewayError(err)
	}

	expiresAt := s.now().Add(loginBrowserTTL)
	sealed, err := s.sealBrowserLogin(SealedBrowserLogin{
		Verifier:    verifier,
		State:       state,
		RedirectURI: redirectURI,
		Next:        safeNext,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return LoginBrowserStart{}, err
	}
	observability.FromContext(ctx).Info("login: browser flow started", "kind", browserAuthorizeKind, "expires_in", int(loginBrowserTTL/time.Second))
	return LoginBrowserStart{
		AuthorizeURL: authorizeURL,
		ExpiresIn:    int(loginBrowserTTL / time.Second),
		Sealed:       sealed,
	}, nil
}

// ExchangeBrowserLogin completes a browser login: unseal the cookie's flow
// state, bind it to the callback (constant-time state match, unexpired),
// exchange the authorization code at the gateway, and run the same login
// transaction the device flow uses. Every failure that indicts the flow
// state surfaces as ErrBrowserFlowState; gateway rejections wrap the
// gateway sentinels (ErrAiandInvalidGrant for a spent or unknown code).
func (s *LoginService) ExchangeBrowserLogin(ctx context.Context, state, code string, sealed []byte) (LoginBrowserResult, error) {
	startedAt := time.Now()
	flow, err := s.openBrowserLogin(sealed)
	if err != nil {
		return LoginBrowserResult{}, err
	}
	if subtle.ConstantTimeCompare([]byte(flow.State), []byte(state)) != 1 {
		return LoginBrowserResult{}, fmt.Errorf("%w: state mismatch", ErrBrowserFlowState)
	}
	if s.now().After(flow.ExpiresAt) {
		return LoginBrowserResult{}, fmt.Errorf("%w: flow expired", ErrBrowserFlowState)
	}

	tokenStartedAt := time.Now()
	token, err := s.gateway.ExchangeCode(ctx, AiandCodeExchangeParams{
		Code:         code,
		CodeVerifier: flow.Verifier,
		RedirectURI:  flow.RedirectURI,
	})
	tokenExchange := time.Since(tokenStartedAt)
	if err != nil {
		if errors.Is(err, ErrAiandInvalidGrant) || errors.Is(err, ErrAiandGatewayUnavailable) {
			return LoginBrowserResult{}, err
		}
		return LoginBrowserResult{}, aiandGatewayError(err)
	}

	identityStartedAt := time.Now()
	identity, err := s.resolveIdentity(ctx, token)
	identityResolution := time.Since(identityStartedAt)
	if err != nil {
		return LoginBrowserResult{}, err
	}

	txStartedAt := time.Now()
	grant, _, err := s.completeLoginTx(ctx, "", identity, token)
	transaction := time.Since(txStartedAt)
	if err != nil {
		return LoginBrowserResult{}, err
	}
	observability.FromContext(ctx).Info("login: browser exchange authorized", "kind", browserExchangeKind, "token_exchange_ms", tokenExchange.Milliseconds(), "identity_ms", identityResolution.Milliseconds(), "transaction_ms", transaction.Milliseconds(), "total_ms", time.Since(startedAt).Milliseconds())
	return LoginBrowserResult{
		Grant:   grant,
		Email:   identity.Email,
		OrgName: identity.OrgName,
		Next:    flow.Next,
	}, nil
}

// sealBrowserLogin encrypts the flow-state blob with the login service's
// encryptor. external_id is empty — like the device-code seal, the blob does
// not yet belong to an account — and the purpose is browserLoginAADLabel.
func (s *LoginService) sealBrowserLogin(flow SealedBrowserLogin) ([]byte, error) {
	plaintext, err := json.Marshal(flow)
	if err != nil {
		return nil, fmt.Errorf("encode browser flow state: %w", err)
	}
	ciphertext, err := s.encryptor.Encrypt(plaintext, "", browserLoginAADLabel)
	if err != nil {
		return nil, fmt.Errorf("seal browser flow state: %w", err)
	}
	return ciphertext, nil
}

// openBrowserLogin decrypts and decodes the sealed flow-state blob. Any
// failure is a flow-state failure: the cookie was tampered with, minted by a
// different keyset, or carries the wrong AAD.
func (s *LoginService) openBrowserLogin(sealed []byte) (SealedBrowserLogin, error) {
	if len(sealed) == 0 {
		return SealedBrowserLogin{}, fmt.Errorf("%w: missing flow cookie", ErrBrowserFlowState)
	}
	plaintext, err := s.encryptor.Decrypt(sealed, "", browserLoginAADLabel)
	if err != nil {
		return SealedBrowserLogin{}, fmt.Errorf("%w: undecryptable flow cookie", ErrBrowserFlowState)
	}
	var flow SealedBrowserLogin
	if err := json.Unmarshal(plaintext, &flow); err != nil {
		return SealedBrowserLogin{}, fmt.Errorf("%w: malformed flow cookie", ErrBrowserFlowState)
	}
	if flow.Verifier == "" || flow.State == "" || flow.RedirectURI == "" {
		return SealedBrowserLogin{}, fmt.Errorf("%w: incomplete flow cookie", ErrBrowserFlowState)
	}
	return flow, nil
}
