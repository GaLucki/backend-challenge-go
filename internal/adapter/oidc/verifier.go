package oidcadapter

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
)

// Verifier owns one discovery result and one concurrency-safe JWKS cache.
// Initialize must succeed before protected HTTP traffic is served.
type Verifier struct {
	cfg    config.Config
	client *http.Client
	now    func() time.Time
	mu     sync.RWMutex
	jwt    *oidc.IDTokenVerifier
}

func NewVerifier(cfg config.Config) (*Verifier, error) {
	if err := cfg.ValidateOIDC(); err != nil {
		return nil, err
	}
	return &Verifier{cfg: cfg, client: &http.Client{Timeout: cfg.OIDCHTTPTimeout}, now: time.Now}, nil
}

func (v *Verifier) Initialize(ctx context.Context) error {
	if !v.cfg.OIDCEnabled {
		return nil
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, v.client), v.cfg.OIDCIssuerURL)
	if err != nil {
		return identity.ErrUnauthorized
	}
	// Keep the HTTP client but not the startup deadline for later JWKS refreshes.
	jwt := provider.VerifierContext(oidc.ClientContext(context.Background(), v.client), &oidc.Config{
		ClientID: v.cfg.OIDCAudience, SkipClientIDCheck: v.cfg.OIDCAudience == "",
		SupportedSigningAlgs: []string{oidc.RS256}, Now: v.now,
	})
	v.mu.Lock()
	v.jwt = jwt
	v.mu.Unlock()
	return nil
}

type accessClaims struct {
	Subject     string `json:"sub"`
	ClientID    string `json:"azp"`
	ProviderID  string `json:"provider_id"`
	Type        string `json:"typ"`
	ExpiresAt   int64  `json:"exp"`
	NotBefore   *int64 `json:"nbf"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (v *Verifier) Authenticate(ctx context.Context, raw string) (identity.Principal, error) {
	v.mu.RLock()
	jwt := v.jwt
	v.mu.RUnlock()
	if jwt == nil || raw == "" {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	token, err := jwt.Verify(ctx, raw)
	if err != nil {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	var claims accessClaims
	if err := token.Claims(&claims); err != nil {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	// go-oidc validates issuer/audience/RS256/signature/expiry. Access-token
	// constraints and strict nbf are checked here (library nbf allows 5m skew).
	now := v.now().Unix()
	if !validID(claims.Subject) || !validID(claims.ClientID) || claims.Type != "Bearer" || claims.ExpiresAt <= now || (claims.NotBefore != nil && *claims.NotBefore > now) {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	p := identity.Principal{Subject: claims.Subject, ClientID: claims.ClientID, ProviderID: claims.ProviderID, Roles: claims.RealmAccess.Roles}
	p.Internal = p.HasRole(identity.RoleInternal)
	if (p.HasRole(identity.RoleProvider) && !validID(p.ProviderID)) || (p.ProviderID != "" && !validID(p.ProviderID)) || (p.Internal && (p.ProviderID != "" || p.HasRole(identity.RoleProvider))) {
		return identity.Principal{}, identity.ErrUnauthorized
	}
	return p, nil
}

func validID(id string) bool {
	return id != "" && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\r\n\t")
}
