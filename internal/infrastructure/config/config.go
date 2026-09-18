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
	defaultHTTPAddr         = ":8080"
	defaultShutdownTimeout  = 10 * time.Second
	defaultLogLevel         = "info"
	defaultAWSRegion        = "us-east-1"
	defaultSQSWagerQueue    = "wager-transactions.fifo"
	defaultSQSWagerDLQ      = "wager-transactions-dlq.fifo"
	defaultSQSVisibility    = 30 * time.Second
	defaultSQSWaitTime      = 10 * time.Second
	defaultSQSMaxMessages   = 1
	defaultSQSBackoff       = 5 * time.Second
	defaultReferencePoll    = time.Second
	defaultReferenceMax     = 10
	defaultReferenceBackoff = time.Second
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

	AWSRegion                 string
	AWSEndpointURL            string
	AWSAccessKeyID            string
	AWSSecretAccessKey        string
	SQSWagerQueue             string
	SQSWagerDLQ               string
	SQSVisibilityTimeout      time.Duration
	SQSWaitTime               time.Duration
	SQSMaxMessages            int32
	SQSRetryVisibilityBackoff time.Duration
	ReferencePollInterval     time.Duration
	ReferenceMaxAttempts      int
	ReferenceBackoff          time.Duration
}

// Load reads and validates the process configuration from the environment.
//
// OIDCIssuer and OIDCJWKSURL are separate on purpose: the issuer must match the
// iss claim byte for byte, while the JWKS endpoint may be reached through an
// internal address that the issuer does not expose.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:                  valueOr(os.Getenv("HTTP_ADDR"), defaultHTTPAddr),
		DatabaseURL:               strings.TrimSpace(os.Getenv("DATABASE_URL")),
		KeycloakRealm:             strings.TrimSpace(os.Getenv("KEYCLOAK_REALM")),
		OIDCIssuer:                strings.TrimSpace(os.Getenv("OIDC_ISSUER")),
		OIDCJWKSURL:               strings.TrimSpace(os.Getenv("OIDC_JWKS_URL")),
		OAuthAudience:             strings.TrimSpace(os.Getenv("OAUTH_AUDIENCE")),
		LogLevel:                  valueOr(os.Getenv("LOG_LEVEL"), defaultLogLevel),
		ShutdownTimeout:           defaultShutdownTimeout,
		AWSRegion:                 valueOr(os.Getenv("AWS_REGION"), defaultAWSRegion),
		AWSEndpointURL:            strings.TrimSpace(os.Getenv("AWS_ENDPOINT_URL")),
		AWSAccessKeyID:            valueOr(os.Getenv("AWS_ACCESS_KEY_ID"), "test"),
		AWSSecretAccessKey:        valueOr(os.Getenv("AWS_SECRET_ACCESS_KEY"), "test"),
		SQSWagerQueue:             valueOr(os.Getenv("SQS_WAGER_QUEUE"), defaultSQSWagerQueue),
		SQSWagerDLQ:               valueOr(os.Getenv("SQS_WAGER_DLQ"), defaultSQSWagerDLQ),
		SQSVisibilityTimeout:      defaultSQSVisibility,
		SQSWaitTime:               defaultSQSWaitTime,
		SQSMaxMessages:            defaultSQSMaxMessages,
		SQSRetryVisibilityBackoff: defaultSQSBackoff,
		ReferencePollInterval:     defaultReferencePoll,
		ReferenceMaxAttempts:      defaultReferenceMax,
		ReferenceBackoff:          defaultReferenceBackoff,
	}
	if raw := strings.TrimSpace(os.Getenv("HTTP_SHUTDOWN_TIMEOUT")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("%w: HTTP_SHUTDOWN_TIMEOUT=%q", ErrInvalidConfiguration, raw)
		}
		cfg.ShutdownTimeout = parsed
	}
	if raw := strings.TrimSpace(os.Getenv("SQS_VISIBILITY_TIMEOUT_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 || seconds > 43200 {
			return Config{}, fmt.Errorf("%w: SQS_VISIBILITY_TIMEOUT_SECONDS=%q", ErrInvalidConfiguration, raw)
		}
		cfg.SQSVisibilityTimeout = time.Duration(seconds) * time.Second
	}
	if raw := strings.TrimSpace(os.Getenv("SQS_WAIT_TIME_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 || seconds > 20 {
			return Config{}, fmt.Errorf("%w: SQS_WAIT_TIME_SECONDS=%q", ErrInvalidConfiguration, raw)
		}
		cfg.SQSWaitTime = time.Duration(seconds) * time.Second
	}
	if raw := strings.TrimSpace(os.Getenv("SQS_MAX_MESSAGES")); raw != "" {
		count, err := strconv.Atoi(raw)
		if err != nil || count <= 0 || count > 10 {
			return Config{}, fmt.Errorf("%w: SQS_MAX_MESSAGES=%q", ErrInvalidConfiguration, raw)
		}
		cfg.SQSMaxMessages = int32(count)
	}
	if raw := strings.TrimSpace(os.Getenv("SQS_RETRY_VISIBILITY_BACKOFF_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 || seconds > 43200 {
			return Config{}, fmt.Errorf("%w: SQS_RETRY_VISIBILITY_BACKOFF_SECONDS=%q", ErrInvalidConfiguration, raw)
		}
		cfg.SQSRetryVisibilityBackoff = time.Duration(seconds) * time.Second
	}
	if raw := strings.TrimSpace(os.Getenv("REFERENCE_POLL_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("%w: REFERENCE_POLL_INTERVAL=%q", ErrInvalidConfiguration, raw)
		}
		cfg.ReferencePollInterval = parsed
	}
	if raw := strings.TrimSpace(os.Getenv("REFERENCE_MAX_ATTEMPTS")); raw != "" {
		attempts, err := strconv.Atoi(raw)
		if err != nil || attempts < 1 {
			return Config{}, fmt.Errorf("%w: REFERENCE_MAX_ATTEMPTS=%q", ErrInvalidConfiguration, raw)
		}
		cfg.ReferenceMaxAttempts = attempts
	}
	if raw := strings.TrimSpace(os.Getenv("REFERENCE_BACKOFF")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < 0 {
			return Config{}, fmt.Errorf("%w: REFERENCE_BACKOFF=%q", ErrInvalidConfiguration, raw)
		}
		cfg.ReferenceBackoff = parsed
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
	if strings.TrimSpace(cfg.AWSRegion) == "" || strings.TrimSpace(cfg.SQSWagerQueue) == "" || strings.TrimSpace(cfg.SQSWagerDLQ) == "" {
		return Config{}, fmt.Errorf("%w: AWS_REGION and SQS queue names are required", ErrInvalidConfiguration)
	}
	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("%w: LOG_LEVEL=%q", ErrInvalidConfiguration, cfg.LogLevel)
	}
	return cfg, nil
}

// SQSVisibilityTimeoutSeconds returns the configured broker visibility budget.
func (c Config) SQSVisibilityTimeoutSeconds() int32 {
	return int32(c.SQSVisibilityTimeout / time.Second)
}

// SQSWaitTimeSeconds returns the configured long-poll duration.
func (c Config) SQSWaitTimeSeconds() int32 { return int32(c.SQSWaitTime / time.Second) }

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
