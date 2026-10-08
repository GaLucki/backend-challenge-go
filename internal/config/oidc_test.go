package config

import (
	"testing"
	"time"
)

func TestOIDCConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://wagering:wagering@localhost:5432/wagering?sslmode=disable")
	t.Setenv("APP_ENV", "test")
	t.Setenv("OIDC_ENABLED", "true")
	t.Setenv("OIDC_ISSUER_URL", "http://localhost:8081/realms/jungle-gaming")
	t.Setenv("OIDC_AUDIENCE", "wagering-api")
	t.Setenv("OIDC_HTTP_TIMEOUT", "2s")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.OIDCEnabled || c.OIDCAudience != "wagering-api" || c.OIDCHTTPTimeout != 2*time.Second {
		t.Fatal("OIDC configuration not loaded")
	}
	for key, value := range map[string]string{"OIDC_ENABLED": "bad", "OIDC_ISSUER_URL": "http://user:secret@localhost/realm", "OIDC_HTTP_TIMEOUT": "-1s"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid OIDC config accepted")
			}
		})
	}
	c.AppEnv = "production"
	if c.ValidateOIDC() == nil {
		t.Fatal("production allowed HTTP issuer")
	}
	c.OIDCIssuerURL = "https://idp.example/realms/jungle-gaming"
	if err := c.ValidateOIDC(); err != nil {
		t.Fatal(err)
	}
	// Operational hardening forbids disabling audience validation in production.
	c.OIDCAudience = ""
	if c.ValidateOIDC() == nil {
		t.Fatal("production allowed audience bypass")
	}
	// Preserve explicit empty-audience compatibility for development/test only.
	c.AppEnv = "test"
	if err := c.ValidateOIDC(); err != nil {
		t.Fatal(err)
	}
}
