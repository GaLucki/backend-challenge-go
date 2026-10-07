package httpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/identity"
)

type authDouble struct{}

func (authDouble) Authenticate(_ context.Context, raw string) (identity.Principal, error) {
	p := identity.Principal{Subject: "subject", ClientID: raw}
	switch raw {
	case "provider":
		p.ProviderID = "provider-a"
		p.Roles = []string{identity.RoleProvider}
	case "internal":
		p.Internal = true
		p.Roles = []string{identity.RoleInternal}
	case "no-role":
	default:
		return identity.Principal{}, identity.ErrUnauthorized
	}
	return p, nil
}

func TestBearerMiddlewareRejectsMalformedHeaders(t *testing.T) {
	m := NewAuthMiddleware(authDouble{}, identity.NewAuthorizer())
	h := m.Authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("malformed header reached handler") }))
	for _, headers := range [][]string{nil, {""}, {"Bearer"}, {"Bearer "}, {"Basic provider"}, {"Bearer  provider"}, {"Bearer\tprovider"}, {"Bearer provider extra"}, {"Bearer provider,Bearer internal"}, {"Bearer provider", "Bearer internal"}, {"Bearer invalid"}} {
		r := httptest.NewRequest("GET", "/probe", nil)
		for _, v := range headers {
			r.Header.Add("Authorization", v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") != "Bearer" || !strings.Contains(w.Body.String(), "UNAUTHORIZED") {
			t.Fatalf("headers %q: status %d", headers, w.Code)
		}
		if strings.Contains(w.Body.String(), "provider") || strings.Contains(w.Body.String(), "internal") {
			t.Fatal("credential leaked")
		}
	}
}

func TestAuthenticationAndAuthorizationStatusAndContext(t *testing.T) {
	m := NewAuthMiddleware(authDouble{}, identity.NewAuthorizer())
	for _, tc := range []struct {
		token    string
		path     string
		internal bool
		status   int
	}{
		{"provider", "provider-a", false, 200}, {"provider", "provider-b", false, 403},
		{"provider", "", true, 403}, {"internal", "", true, 200},
		{"no-role", "provider-a", false, 403}, {"no-role", "", true, 403},
		{"invalid", "provider-a", false, 401},
	} {
		t.Run(tc.token+tc.path, func(t *testing.T) {
			probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p, ok := identity.FromContext(r.Context())
				if !ok || p.ClientID != tc.token {
					t.Error("principal missing from context")
				}
				w.WriteHeader(200)
			})
			var h http.Handler = m.AuthorizeProvider(func(*http.Request) string { return tc.path }, probe)
			if tc.internal {
				h = m.RequireInternal(probe)
			}
			h = m.Authenticate(h)
			r := httptest.NewRequest("GET", "/probe", nil)
			r.Header.Set("Authorization", "bEaReR "+tc.token)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d want %d", w.Code, tc.status)
			}
		})
	}
}
