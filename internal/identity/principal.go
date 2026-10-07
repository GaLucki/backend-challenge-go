// Package identity defines authenticated identities and application authorization
// policy without depending on JWT, HTTP, or an identity provider SDK.
package identity

import (
	"context"
	"errors"
	"slices"
)

var (
	ErrUnauthorized = errors.New("authentication required")
	ErrForbidden    = errors.New("access forbidden")
)

const RoleProvider = "provider"
const RoleInternal = "internal"

type Principal struct {
	Subject    string   `json:"subject"`
	ClientID   string   `json:"clientId"`
	ProviderID string   `json:"providerId,omitempty"`
	Roles      []string `json:"roles"`
	Internal   bool     `json:"internal"`
}

func (p Principal) HasRole(role string) bool { return slices.Contains(p.Roles, role) }
func (p Principal) Authenticated() bool      { return p.Subject != "" && p.ClientID != "" }

type contextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	p.Roles = slices.Clone(p.Roles)
	return context.WithValue(ctx, contextKey{}, p)
}
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	p.Roles = slices.Clone(p.Roles)
	return p, ok && p.Authenticated()
}

// Authenticator is implemented by an infrastructure adapter; it returns only
// sanitized errors and never places the raw credential in the context.
type Authenticator interface {
	Authenticate(context.Context, string) (Principal, error)
}

type Authorizer struct{}

func NewAuthorizer() *Authorizer { return &Authorizer{} }

func (*Authorizer) RequireProvider(p Principal) error {
	if !p.Authenticated() {
		return ErrUnauthorized
	}
	if p.Internal || p.HasRole(RoleInternal) || !p.HasRole(RoleProvider) || p.ProviderID == "" {
		return ErrForbidden
	}
	return nil
}

func (*Authorizer) RequireInternal(p Principal) error {
	if !p.Authenticated() {
		return ErrUnauthorized
	}
	if !p.Internal || !p.HasRole(RoleInternal) || p.HasRole(RoleProvider) || p.ProviderID != "" {
		return ErrForbidden
	}
	return nil
}

// Internal identities may read any explicitly selected provider. Provider
// writes use RequireProvider separately and cannot impersonate a provider.
func (a *Authorizer) AuthorizeProvider(p Principal, requestedProviderID string) error {
	if a.RequireInternal(p) == nil && requestedProviderID != "" {
		return nil
	}
	if err := a.RequireProvider(p); err != nil {
		return err
	}
	if requestedProviderID == "" || requestedProviderID != p.ProviderID {
		return ErrForbidden
	}
	return nil
}
