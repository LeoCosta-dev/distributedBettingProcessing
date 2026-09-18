package keycloak

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestVerifierAgainstRealKeycloak(t *testing.T) {
	if os.Getenv("RUN_KEYCLOAK_INTEGRATION") != "1" {
		t.Skip("RUN_KEYCLOAK_INTEGRATION is not 1")
	}
	issuer := envOrDefault("OIDC_ISSUER", "http://localhost:8082/realms/wagering")
	jwksURL := envOrDefault("OIDC_JWKS_URL", "http://localhost:8082/realms/wagering/protocol/openid-connect/certs")
	audience := envOrDefault("OAUTH_AUDIENCE", "wagering-api")
	tokenURL := envOrDefault("KEYCLOAK_TOKEN_URL", strings.TrimRight(issuer, "/")+"/protocol/openid-connect/token")
	clientID := envOrDefault("OAUTH_CLIENT_ID", "wagering-api")
	clientSecret := envOrDefault("OAUTH_CLIENT_SECRET", "change-me")
	username := envOrDefault("KEYCLOAK_TEST_USERNAME", "provider-alpha")
	password := envOrDefault("KEYCLOAK_TEST_PASSWORD", "dev-only-pass")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	form := url.Values{
		"grant_type":    {"password"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"username":      {username},
		"password":      {password},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("Keycloak token endpoint = %d: %s", response.StatusCode, body)
	}
	var tokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&tokenResponse); err != nil {
		t.Fatal(err)
	}
	if tokenResponse.AccessToken == "" {
		t.Fatal("Keycloak returned an empty access token")
	}

	verifier, err := NewVerifier(ctx, issuer, jwksURL, audience)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := verifier.Verify(ctx, tokenResponse.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject == "" || identity.ProviderID == "" || !identity.HasRole(RoleProvider) {
		t.Fatalf("identity = %+v", identity)
	}

	wrongAudience, err := NewVerifier(ctx, issuer, jwksURL, "wrong-audience")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongAudience.Verify(ctx, tokenResponse.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong audience error = %v, want ErrInvalidToken", err)
	}

	wrongIssuer, err := NewVerifier(ctx, issuer+"/wrong", jwksURL, audience)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongIssuer.Verify(ctx, tokenResponse.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong issuer error = %v, want ErrInvalidToken", err)
	}

	if _, err := verifier.Verify(ctx, tokenResponse.AccessToken+"tampered"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("tampered token error = %v, want ErrInvalidToken", err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
