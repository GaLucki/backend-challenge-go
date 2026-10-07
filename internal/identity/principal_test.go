package identity

import (
	"context"
	"errors"
	"testing"
)

func TestAuthorizationPolicy(t *testing.T) {
	a := NewAuthorizer()
	p := Principal{Subject: "a", ClientID: "client-a", ProviderID: "a", Roles: []string{RoleProvider}}
	i := Principal{Subject: "internal", ClientID: "internal", Internal: true, Roles: []string{RoleInternal}}
	if a.AuthorizeProvider(p, "a") != nil || a.RequireInternal(i) != nil || a.AuthorizeProvider(i, "b") != nil {
		t.Fatal("authorized action rejected")
	}
	for _, err := range []error{a.AuthorizeProvider(p, "b"), a.AuthorizeProvider(p, ""), a.RequireInternal(p), a.RequireProvider(i), a.RequireInternal(Principal{Subject: "spoof", ClientID: "spoof", Internal: true})} {
		if !errors.Is(err, ErrForbidden) {
			t.Fatal("expected forbidden", err)
		}
	}
	if !errors.Is(a.AuthorizeProvider(Principal{}, "a"), ErrUnauthorized) {
		t.Fatal("anonymous principal authorized")
	}
}

func TestPrincipalContextIsTypedAndCopiesRoles(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("anonymous context has principal")
	}
	p := Principal{Subject: "a", ClientID: "a", Roles: []string{RoleProvider}}
	ctx := WithPrincipal(context.Background(), p)
	p.Roles[0] = RoleInternal
	got, ok := FromContext(ctx)
	if !ok || !got.HasRole(RoleProvider) || got.HasRole(RoleInternal) {
		t.Fatal("context principal was mutated")
	}
	got.Roles[0] = RoleInternal
	again, _ := FromContext(ctx)
	if !again.HasRole(RoleProvider) {
		t.Fatal("context roles escaped")
	}
}
