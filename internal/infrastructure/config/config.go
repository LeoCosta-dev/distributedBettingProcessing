package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidConfiguration classifies every configuration failure so the
// composition layer can fail fast on startup without a panic.
var ErrInvalidConfiguration = errors.New("invalid configuration")

const (
	defaultHTTPAddr        = ":8080"
	defaultShutdownTimeout = 10 * time.Second
	defaultLogLevel        = "info"
)

// Config holds the transport and dependency configuration of the process.
// It is loaded only from the environment; secrets are never committed.
type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	KeycloakRealm   string
	OIDCIssuer      string
	OIDCJWKSURL     string
	OAuthAudience   string
	ShutdownTimeout time.Duration
	LogLevel        string
}

// Load reads and validates the process configuration from the environment.
//
// OIDCIssuer and OIDCJWKSURL are separate on purpose: the issuer must match the
// iss claim byte for byte, while the JWKS endpoint may be reached through an
// internal address that the issuer does not expose.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:        valueOr(os.Getenv("HTTP_ADDR"), defaultHTTPAddr),
		DatabaseURL:     strings.TrimSpace(os.Getenv("DATABASE_URL")),
		KeycloakRealm:   strings.TrimSpace(os.Getenv("KEYCLOAK_REALM")),
		OIDCIssuer:      strings.TrimSpace(os.Getenv("OIDC_ISSUER")),
		OIDCJWKSURL:     strings.TrimSpace(os.Getenv("OIDC_JWKS_URL")),
		OAuthAudience:   strings.TrimSpace(os.Getenv("OAUTH_AUDIENCE")),
		LogLevel:        valueOr(os.Getenv("LOG_LEVEL"), defaultLogLevel),
		ShutdownTimeout: defaultShutdownTimeout,
	}
	if raw := strings.TrimSpace(os.Getenv("HTTP_SHUTDOWN_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("%w: HTTP_SHUTDOWN_TIMEOUT=%q", ErrInvalidConfiguration, raw)
		}
		cfg.ShutdownTimeout = parsed
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{"DATABASE_URL", cfg.DatabaseURL},
		{"OIDC_ISSUER", cfg.OIDCIssuer},
		{"OIDC_JWKS_URL", cfg.OIDCJWKSURL},
		{"OAUTH_AUDIENCE", cfg.OAuthAudience},
	} {
		if required.value == "" {
			return Config{}, fmt.Errorf("%w: %s is required", ErrInvalidConfiguration, required.name)
		}
	}
	if err := validateOIDCRealmConsistency(cfg.OIDCIssuer, cfg.OIDCJWKSURL, cfg.KeycloakRealm); err != nil {
		return Config{}, err
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("%w: LOG_LEVEL=%q", ErrInvalidConfiguration, cfg.LogLevel)
	}
	return cfg, nil
}

// validateOIDCRealmConsistency allows issuer and JWKS to use different hosts
// while rejecting a configuration that names different Keycloak realms. URLs
// for non-Keycloak providers may omit /realms/<name> and remain valid.
func validateOIDCRealmConsistency(issuer, jwks, configuredRealm string) error {
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("%w: OIDC_ISSUER is not a valid URL", ErrInvalidConfiguration)
	}
	jwksURL, err := url.Parse(jwks)
	if err != nil {
		return fmt.Errorf("%w: OIDC_JWKS_URL is not a valid URL", ErrInvalidConfiguration)
	}
	issuerRealm, issuerIsKeycloak := realmFromPath(issuerURL.Path)
	jwksRealm, jwksIsKeycloak := realmFromPath(jwksURL.Path)
	if issuerIsKeycloak && jwksIsKeycloak && issuerRealm != jwksRealm {
		return fmt.Errorf("%w: OIDC_ISSUER realm %q differs from OIDC_JWKS_URL realm %q", ErrInvalidConfiguration, issuerRealm, jwksRealm)
	}
	if configuredRealm != "" {
		if issuerIsKeycloak && issuerRealm != configuredRealm {
			return fmt.Errorf("%w: OIDC_ISSUER realm %q differs from KEYCLOAK_REALM %q", ErrInvalidConfiguration, issuerRealm, configuredRealm)
		}
		if jwksIsKeycloak && jwksRealm != configuredRealm {
			return fmt.Errorf("%w: OIDC_JWKS_URL realm %q differs from KEYCLOAK_REALM %q", ErrInvalidConfiguration, jwksRealm, configuredRealm)
		}
	}
	return nil
}

func realmFromPath(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for index := 0; index+1 < len(parts); index++ {
		if parts[index] == "realms" && parts[index+1] != "" {
			return parts[index+1], true
		}
	}
	return "", false
}

// LogLevelValue maps the configured level to log/slog verbosity.
func (c Config) LogLevelValue() int {
	switch c.LogLevel {
	case "debug":
		return -4
	case "warn":
		return 4
	case "error":
		return 8
	default:
		return 0
	}
}

// ShutdownTimeoutSeconds exposes the drain budget for documentation and tests.
func (c Config) ShutdownTimeoutSeconds() string {
	return strconv.FormatFloat(c.ShutdownTimeout.Seconds(), 'f', -1, 64)
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
