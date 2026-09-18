package config

import (
	"errors"
	"testing"
	"time"
)

func setRequiredEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://example/wagering")
	t.Setenv("KEYCLOAK_REALM", "")
	t.Setenv("OIDC_ISSUER", "https://issuer.example/realms/wagering")
	t.Setenv("OIDC_JWKS_URL", "https://keycloak.example/certs")
	t.Setenv("OAUTH_AUDIENCE", "wagering-api")
}

func TestLoadRequiresDependenciesAndAppliesDefaults(t *testing.T) {
	setRequiredEnvironment(t)
	for _, name := range []string{"HTTP_ADDR", "HTTP_SHUTDOWN_TIMEOUT", "LOG_LEVEL"} {
		t.Setenv(name, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != defaultHTTPAddr || cfg.ShutdownTimeout != defaultShutdownTimeout || cfg.LogLevel != defaultLogLevel {
		t.Fatalf("config defaults = %+v", cfg)
	}
	if cfg.ShutdownTimeoutSeconds() != "10" {
		t.Fatalf("shutdown seconds = %q", cfg.ShutdownTimeoutSeconds())
	}
}

func TestLoadReadsOIDCAndShutdownConfiguration(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("HTTP_ADDR", "127.0.0.1:18080")
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "3s")
	t.Setenv("LOG_LEVEL", "debug")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OIDCIssuer != "https://issuer.example/realms/wagering" || cfg.OIDCJWKSURL != "https://keycloak.example/certs" || cfg.OAuthAudience != "wagering-api" {
		t.Fatalf("OIDC config = %+v", cfg)
	}
	if cfg.HTTPAddr != "127.0.0.1:18080" || cfg.ShutdownTimeout != 3*time.Second || cfg.LogLevelValue() != -4 {
		t.Fatalf("runtime config = %+v", cfg)
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T)
	}{
		{name: "missing database", setup: func(t *testing.T) {
			setRequiredEnvironment(t)
			t.Setenv("DATABASE_URL", "")
		}},
		{name: "invalid timeout", setup: func(t *testing.T) {
			setRequiredEnvironment(t)
			t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "0s")
		}},
		{name: "invalid log level", setup: func(t *testing.T) {
			setRequiredEnvironment(t)
			t.Setenv("LOG_LEVEL", "trace")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.setup(t)
			_, err := Load()
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("error = %v, want ErrInvalidConfiguration", err)
			}
		})
	}
}

func TestLoadRejectsDifferentOIDCRealms(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("OIDC_ISSUER", "http://localhost:8082/realms/wagering")
	t.Setenv("OIDC_JWKS_URL", "http://keycloak:8080/realms/other/protocol/openid-connect/certs")

	if _, err := Load(); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v, want ErrInvalidConfiguration", err)
	}
}

func TestLoadRejectsOIDCRealmDifferentFromKeycloakRealm(t *testing.T) {
	setRequiredEnvironment(t)
	t.Setenv("KEYCLOAK_REALM", "other")
	t.Setenv("OIDC_ISSUER", "http://localhost:8082/realms/wagering")
	t.Setenv("OIDC_JWKS_URL", "http://keycloak:8080/realms/wagering/protocol/openid-connect/certs")

	if _, err := Load(); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("error = %v, want ErrInvalidConfiguration", err)
	}
}
