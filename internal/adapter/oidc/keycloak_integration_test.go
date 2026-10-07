package oidcadapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	httpadapter "github.com/junglegaming/backend-challenge-go/internal/adapter/http"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

func TestIntegrationKeycloakTokensAndValidation(t *testing.T) {
	issuer := os.Getenv("TEST_OIDC_ISSUER_URL")
	if issuer == "" {
		t.Skip("set TEST_OIDC_ISSUER_URL for real Keycloak integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second}
	if health := os.Getenv("TEST_KEYCLOAK_HEALTH_URL"); health != "" {
		req, _ := http.NewRequestWithContext(ctx, "GET", health, nil)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("Keycloak health request failed")
		}
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("Keycloak health status %d", response.StatusCode)
		}
	}
	cfg := config.Config{AppEnv: "test", OIDCEnabled: true, OIDCIssuerURL: issuer, OIDCAudience: "wagering-api", OIDCHTTPTimeout: 5 * time.Second}
	v, err := NewVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Initialize(ctx); err != nil {
		t.Fatal("real discovery failed")
	}
	var tokenA string
	subjects := map[string]bool{}
	for _, id := range []string{"provider-a", "provider-b", "internal-service"} {
		grant := clientcredentials.Config{ClientID: id, ClientSecret: "local-dev-" + id + "-secret", TokenURL: strings.TrimRight(issuer, "/") + "/protocol/openid-connect/token"}
		token, err := grant.Token(context.WithValue(ctx, oauth2.HTTPClient, client))
		if err != nil {
			t.Fatalf("client_credentials failed for %s", id)
		}
		p, err := v.Authenticate(ctx, token.AccessToken)
		if err != nil {
			t.Fatalf("real token rejected for %s", id)
		}
		if p.ClientID != id || subjects[p.Subject] {
			t.Fatal("identities must have distinct subjects and correct client IDs")
		}
		subjects[p.Subject] = true
		if id == "internal-service" {
			if identity.NewAuthorizer().RequireInternal(p) != nil || p.ProviderID != "" {
				t.Fatal("internal mapping invalid")
			}
		} else if p.ProviderID != id || identity.NewAuthorizer().RequireProvider(p) != nil {
			t.Fatal("provider mapping invalid")
		}
		m := httpadapter.NewAuthMiddleware(v, identity.NewAuthorizer())
		r := httptest.NewRequest("GET", "/internal", nil)
		r.Header.Set("Authorization", "Bearer "+token.AccessToken)
		w := httptest.NewRecorder()
		m.Authenticate(m.RequireInternal(http.HandlerFunc(httpadapter.ProbeHandler))).ServeHTTP(w, r)
		want := 403
		if id == "internal-service" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("%s internal status %d", id, w.Code)
		}
		if id == "provider-a" {
			tokenA = token.AccessToken
		}
	}
	for name, raw := range map[string]string{"missing": "", "invalid": "invalid-token"} {
		t.Run(name, func(t *testing.T) {
			m := httpadapter.NewAuthMiddleware(v, identity.NewAuthorizer())
			r := httptest.NewRequest("GET", "/probe", nil)
			if raw != "" {
				r.Header.Set("Authorization", "Bearer "+raw)
			}
			w := httptest.NewRecorder()
			m.Authenticate(http.HandlerFunc(httpadapter.ProbeHandler)).ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("status %d", w.Code)
			}
		})
	}
	token, err := v.jwt.Verify(ctx, tokenA)
	if err != nil {
		t.Fatal("valid real token unexpectedly rejected")
	}
	// Use an injected clock after the actual signed expiry; the real token and
	// its signature/JWKS are untouched and no wall-clock sleep is needed.
	expired, _ := NewVerifier(cfg)
	expired.now = func() time.Time { return token.Expiry.Add(time.Second) }
	if err := expired.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	m := httpadapter.NewAuthMiddleware(expired, identity.NewAuthorizer())
	r := httptest.NewRequest("GET", "/probe", nil)
	r.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	m.Authenticate(http.HandlerFunc(httpadapter.ProbeHandler)).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("expired real token accepted")
	}

	wrongAudience := cfg
	wrongAudience.OIDCAudience = "other-api"
	other, _ := NewVerifier(wrongAudience)
	if err := other.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Authenticate(ctx, tokenA); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("real token accepted for wrong audience")
	}
	// The real remote key set stays the same; only the expected issuer changes.
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, client), issuer)
	if err != nil {
		t.Fatal(err)
	}
	var discovery struct {
		JWKSURL string `json:"jwks_uri"`
	}
	if err := provider.Claims(&discovery); err != nil {
		t.Fatal(err)
	}
	other.jwt = oidc.NewVerifier("https://wrong-issuer.example", oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), discovery.JWKSURL), &oidc.Config{ClientID: "wagering-api", SupportedSigningAlgs: []string{oidc.RS256}})
	if _, err := other.Authenticate(ctx, tokenA); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("real token accepted for wrong issuer")
	}
}
