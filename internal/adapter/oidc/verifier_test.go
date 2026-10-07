package oidcadapter

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
)

type oidcFixture struct {
	server   *httptest.Server
	key      *rsa.PrivateKey
	kid      string
	mu       sync.Mutex
	requests atomic.Int32
	down     atomic.Bool
	now      time.Time
	verifier *Verifier
}

func newFixture(t *testing.T) *oidcFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &oidcFixture{key: key, kid: "key-1", now: time.Now().UTC().Truncate(time.Second)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": f.server.URL, "jwks_uri": f.server.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
			return
		}
		if r.URL.Path != "/keys" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.requests.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: f.kid, Algorithm: "RS256", Use: "sig"}}})
	}))
	t.Cleanup(f.server.Close)
	f.verifier, err = NewVerifier(config.Config{AppEnv: "test", OIDCEnabled: true, OIDCIssuerURL: f.server.URL, OIDCAudience: "wagering-api", OIDCHTTPTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.verifier.now = func() time.Time { return f.now }
	if err := f.verifier.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *oidcFixture) claims() map[string]any {
	return map[string]any{"iss": f.server.URL, "aud": "wagering-api", "sub": "subject-a", "azp": "client-a", "provider_id": "provider-a", "typ": "Bearer", "exp": f.now.Add(time.Minute).Unix(), "realm_access": map[string]any{"roles": []string{"provider"}}}
}

func signToken(t *testing.T, claims map[string]any, key *rsa.PrivateKey, kid string, alg jose.SignatureAlgorithm) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerifierValidatesAccessToken(t *testing.T) {
	f := newFixture(t)
	claims := f.claims()
	claims["nbf"] = f.now.Unix()
	p, err := f.verifier.Authenticate(context.Background(), signToken(t, claims, f.key, f.kid, jose.RS256))
	if err != nil {
		t.Fatal(err)
	}
	if p.Subject != "subject-a" || p.ClientID != "client-a" || p.ProviderID != "provider-a" || p.Internal || !p.HasRole(identity.RoleProvider) {
		t.Fatal("incorrect principal", p)
	}
	claims = f.claims()
	delete(claims, "provider_id")
	claims["realm_access"] = map[string]any{"roles": []string{"internal"}}
	p, err = f.verifier.Authenticate(context.Background(), signToken(t, claims, f.key, f.kid, jose.RS256))
	if err != nil || !p.Internal || p.ProviderID != "" || identity.NewAuthorizer().RequireInternal(p) != nil {
		t.Fatal("internal identity rejected")
	}
	claims = f.claims()
	delete(claims, "realm_access")
	p, err = f.verifier.Authenticate(context.Background(), signToken(t, claims, f.key, f.kid, jose.RS256))
	if err != nil || !errors.Is(identity.NewAuthorizer().RequireProvider(p), identity.ErrForbidden) {
		t.Fatal("valid identity without permission must be authorized separately")
	}
}

func TestVerifierRejectsInvalidClaimsAndSignatures(t *testing.T) {
	f := newFixture(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"expired", func(c map[string]any) { c["exp"] = f.now.Add(-time.Second).Unix() }},
		{"expiry boundary", func(c map[string]any) { c["exp"] = f.now.Unix() }},
		{"missing expiry", func(c map[string]any) { delete(c, "exp") }},
		{"nbf future within library skew", func(c map[string]any) { c["nbf"] = f.now.Add(time.Second).Unix() }},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://other.example" }},
		{"wrong audience", func(c map[string]any) { c["aud"] = "other-api" }},
		{"missing audience", func(c map[string]any) { delete(c, "aud") }},
		{"missing subject", func(c map[string]any) { delete(c, "sub") }},
		{"missing client", func(c map[string]any) { delete(c, "azp") }},
		{"missing provider mapping", func(c map[string]any) { delete(c, "provider_id") }},
		{"invalid provider mapping", func(c map[string]any) { c["provider_id"] = " provider-a" }},
		{"wrong claim type", func(c map[string]any) { c["provider_id"] = 5 }},
		{"ID token instead of access token", func(c map[string]any) { c["typ"] = "ID" }},
		{"ambiguous roles", func(c map[string]any) { c["realm_access"] = map[string]any{"roles": []string{"provider", "internal"}} }},
		{"internal with provider mapping", func(c map[string]any) { c["realm_access"] = map[string]any{"roles": []string{"internal"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := f.claims()
			tc.change(c)
			_, err := f.verifier.Authenticate(context.Background(), signToken(t, c, f.key, f.kid, jose.RS256))
			if !errors.Is(err, identity.ErrUnauthorized) {
				t.Fatal("invalid claims accepted")
			}
		})
	}
	badTokens := map[string]string{
		"wrong signature": signToken(t, f.claims(), other, f.kid, jose.RS256),
		"unknown key":     signToken(t, f.claims(), other, "unknown-key", jose.RS256),
		"wrong algorithm": signToken(t, f.claims(), f.key, f.kid, jose.RS512),
		"malformed":       "invalid",
		"none":            base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"attacker"}`)) + ".",
	}
	for name, raw := range badTokens {
		t.Run(name, func(t *testing.T) {
			_, err := f.verifier.Authenticate(context.Background(), raw)
			if !errors.Is(err, identity.ErrUnauthorized) {
				t.Fatal("invalid credential accepted")
			}
		})
	}
}

func TestJWKSCacheRotationAndOutage(t *testing.T) {
	f := newFixture(t)
	raw := signToken(t, f.claims(), f.key, f.kid, jose.RS256)
	for range 3 {
		if _, err := f.verifier.Authenticate(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}
	if f.requests.Load() != 1 {
		t.Fatal("JWKS not reused")
	}
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.key = newKey
	f.kid = "key-2"
	f.mu.Unlock()
	raw = signToken(t, f.claims(), f.key, f.kid, jose.RS256)
	if _, err := f.verifier.Authenticate(context.Background(), raw); err != nil {
		t.Fatal("rotated key rejected", err)
	}
	if f.requests.Load() != 2 {
		t.Fatal("rotation did not refresh JWKS")
	}
	f.down.Store(true)
	if _, err := f.verifier.Authenticate(context.Background(), raw); err != nil {
		t.Fatal("cached, cryptographically valid token rejected during outage")
	}
	unknown := signToken(t, f.claims(), f.key, "key-3", jose.RS256)
	if _, err := f.verifier.Authenticate(context.Background(), unknown); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("unknown key accepted during outage")
	}
	if err := f.verifier.Initialize(context.Background()); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("discovery outage accepted")
	}
}

func TestUninitializedAndDisabledVerifierFailClosed(t *testing.T) {
	v, err := NewVerifier(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Authenticate(context.Background(), "anything"); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("disabled verifier bypassed validation")
	}
}
