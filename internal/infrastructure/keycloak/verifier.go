package keycloak

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// RoleProvider identifies an authenticated wagering provider identity.
const RoleProvider = "provider"

// RoleInternal identifies the internal wallet administration identity.
const RoleInternal = "internal"

// Identity is the authenticated principal derived exclusively from a token
// issued by the configured IdP.
type Identity struct {
	Subject    string
	Username   string
	ProviderID string
	Roles      []string
}

// HasRole reports whether the token carries the given realm role.
func (i Identity) HasRole(role string) bool {
	for _, current := range i.Roles {
		if current == role {
			return true
		}
	}
	return false
}

var (
	// ErrInvalidToken classifies rejected credentials: bad signature, wrong
	// issuer, wrong audience, expired, not yet valid or malformed claims.
	ErrInvalidToken = errors.New("invalid token")
	// ErrVerifierUnavailable means that the token could not be evaluated because
	// the configured JWKS/IdP dependency was unavailable. It is deliberately
	// distinct from ErrInvalidToken so the transport can fail closed without
	// misclassifying an infrastructure outage as bad credentials.
	ErrVerifierUnavailable = errors.New("oidc verifier unavailable")
)

// Verifier validates access tokens against the IdP JWKS endpoint.
//
// Issuer and JWKS are configured separately on purpose: the issuer must match
// the iss claim byte for byte, while the JWKS endpoint may only be reachable
// through an internal address. Neither issuer nor audience validation is ever
// relaxed to work around networking.
type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

// NewVerifier builds a verifier that checks the RS256 signature against the
// remote JWKS, the issuer, the expiry, the configured audience and an explicit
// zero-skew not-before policy enforced below.
func NewVerifier(ctx context.Context, issuer, jwksURL, audience string) (*Verifier, error) {
	if issuer == "" || jwksURL == "" || audience == "" {
		return nil, fmt.Errorf("%w: issuer, jwks url and audience are required", ErrInvalidToken)
	}
	keySet := oidc.NewRemoteKeySet(ctx, jwksURL)
	return &Verifier{verifier: oidc.NewVerifier(issuer, keySet, &oidc.Config{
		ClientID:             audience,
		SupportedSigningAlgs: []string{oidc.RS256},
	})}, nil
}

// Verify validates the raw bearer token and extracts the authenticated identity.
// A missing provider_id claim is not an error: it yields an identity without
// provider scope, and only provider-scoped endpoints reject it.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (Identity, error) {
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return Identity{}, classifyVerificationError(err)
	}
	var claims struct {
		Subject           string          `json:"sub"`
		PreferredUsername string          `json:"preferred_username"`
		ProviderID        string          `json:"provider_id"`
		NotBefore         json.RawMessage `json:"nbf"`
		RealmAccess       struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := token.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if err := validateNotBefore(claims.NotBefore, time.Now()); err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	return Identity{
		Subject:    claims.Subject,
		Username:   claims.PreferredUsername,
		ProviderID: claims.ProviderID,
		Roles:      claims.RealmAccess.Roles,
	}, nil
}

// validateNotBefore makes the approved policy explicit instead of inheriting
// go-oidc's default future-clock tolerance. NumericDate values must be integer
// seconds and are valid only when nbf is at or before the current instant.
func validateNotBefore(raw json.RawMessage, now time.Time) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value json.Number
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("nbf must be an integer NumericDate: %w", err)
	}
	seconds, err := strconv.ParseInt(value.String(), 10, 64)
	if err != nil {
		return fmt.Errorf("nbf must be an integer NumericDate: %w", err)
	}
	if time.Unix(seconds, 0).After(now) {
		return fmt.Errorf("nbf is in the future")
	}
	return nil
}

func classifyVerificationError(err error) error {
	if isVerifierUnavailable(err) {
		return fmt.Errorf("%w: %v", ErrVerifierUnavailable, err)
	}
	return fmt.Errorf("%w: %v", ErrInvalidToken, err)
}

func isVerifierUnavailable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	message := err.Error()
	// go-oidc intentionally exposes the remote-key refresh boundary in its
	// error text, but does not export a typed error for it. Signature and claim
	// failures use different messages and remain authentication failures.
	return strings.Contains(message, "fetching keys") || strings.Contains(message, "get keys failed")
}
