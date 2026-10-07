package config

import (
	"fmt"
	"net/url"
	"strconv"
	"time"
)

func (c *Config) loadOIDC() error {
	var err error
	c.OIDCEnabled, err = strconv.ParseBool(getEnv("OIDC_ENABLED", "false"))
	if err != nil {
		return fmt.Errorf("invalid OIDC_ENABLED")
	}
	c.OIDCIssuerURL = getEnv("OIDC_ISSUER_URL", "")
	c.OIDCAudience = getEnv("OIDC_AUDIENCE", "wagering-api")
	c.OIDCHTTPTimeout, err = durationEnv("OIDC_HTTP_TIMEOUT", 5*time.Second)
	return err
}

func (c Config) ValidateOIDC() error {
	if !c.OIDCEnabled {
		return nil
	}
	u, err := url.Parse(c.OIDCIssuerURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("OIDC_ISSUER_URL must be an absolute HTTP(S) issuer without credentials, query or fragment")
	}
	if c.AppEnv != "development" && c.AppEnv != "test" && u.Scheme != "https" {
		return fmt.Errorf("OIDC_ISSUER_URL requires HTTPS outside development/test")
	}
	if c.OIDCHTTPTimeout <= 0 {
		return fmt.Errorf("OIDC_HTTP_TIMEOUT must be positive")
	}
	return nil
}
