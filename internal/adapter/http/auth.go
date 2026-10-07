package httpadapter

import (
	"errors"
	"net/http"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/identity"
)

type AuthMiddleware struct {
	verifier   identity.Authenticator
	authorizer *identity.Authorizer
}

func NewAuthMiddleware(verifier identity.Authenticator, authorizer *identity.Authorizer) *AuthMiddleware {
	return &AuthMiddleware{verifier: verifier, authorizer: authorizer}
}

func (m *AuthMiddleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 {
			writeAuthError(w, identity.ErrUnauthorized)
			return
		}
		parts := strings.Split(values[0], " ")
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" || strings.ContainsAny(parts[1], ",\t\r\n") {
			writeAuthError(w, identity.ErrUnauthorized)
			return
		}
		p, err := m.verifier.Authenticate(r.Context(), parts[1])
		if err != nil || !p.Authenticated() {
			writeAuthError(w, identity.ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(identity.WithPrincipal(r.Context(), p)))
	})
}

func (m *AuthMiddleware) RequireInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		if err := m.authorizer.RequireInternal(p); err != nil {
			writeAuthError(w, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *AuthMiddleware) AuthorizeProvider(providerID func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := identity.FromContext(r.Context())
		if err := m.authorizer.AuthorizeProvider(p, providerID(r)); err != nil {
			writeAuthError(w, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, identity.ErrForbidden) {
		WriteError(w, http.StatusForbidden, ErrorCodeForbidden, "access forbidden")
		return
	}
	w.Header().Set("WWW-Authenticate", "Bearer")
	WriteError(w, http.StatusUnauthorized, ErrorCodeUnauthorized, "authentication required")
}

// ProbeHandler exposes only identity/permission checks for phase 8.
func ProbeHandler(w http.ResponseWriter, r *http.Request) {
	p, _ := identity.FromContext(r.Context())
	WriteJSON(w, http.StatusOK, p)
}
