package keycloak

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateNotBeforeUsesExplicitZeroSkew(t *testing.T) {
	now := time.Unix(100, 500000000)
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "absent", raw: "", want: true},
		{name: "past", raw: "99", want: true},
		{name: "exact boundary", raw: "100", want: true},
		{name: "future", raw: "101", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := json.RawMessage(test.raw)
			err := validateNotBefore(raw, now)
			if (err == nil) != test.want {
				t.Fatalf("validateNotBefore(%q) = %v, valid = %v", test.raw, err, test.want)
			}
		})
	}
}

func TestVerifierRejectsFutureNotBeforeDespiteLibraryClockSkew(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer := "https://issuer.example/realms/wagering"
	audience := "wagering-api"
	server := newJWKSFixture(t, key)
	defer server.Close()
	verifier, err := NewVerifier(t.Context(), issuer, server.URL, audience)
	if err != nil {
		t.Fatal(err)
	}

	validToken := signTestToken(t, key, issuer, audience, time.Now().Add(-time.Minute).Unix())
	if _, err := verifier.Verify(t.Context(), validToken); err != nil {
		t.Fatalf("past nbf token = %v", err)
	}
	futureToken := signTestToken(t, key, issuer, audience, time.Now().Add(2*time.Minute).Unix())
	if _, err := verifier.Verify(t.Context(), futureToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("future nbf error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifierDistinguishesJWKSUnavailableFromInvalidSignature(t *testing.T) {
	issuer := "https://issuer.example/realms/wagering"
	audience := "wagering-api"

	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	verifier, err := NewVerifier(t.Context(), issuer, unavailable.URL, audience)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(t.Context(), unsignedTestToken("unavailable-key")); !errors.Is(err, ErrVerifierUnavailable) {
		t.Fatalf("JWKS outage error = %v, want ErrVerifierUnavailable", err)
	}
	unavailable.Close()

	emptyKeys := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer emptyKeys.Close()
	verifier, err = NewVerifier(t.Context(), issuer, emptyKeys.URL, audience)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(t.Context(), unsignedTestToken("invalid-signature-key")); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("invalid signature error = %v, want ErrInvalidToken", err)
	}
}

func newJWKSFixture(t *testing.T, key *rsa.PrivateKey) *httptest.Server {
	t.Helper()
	publicExponent := big.NewInt(int64(key.PublicKey.E)).Bytes()
	keys := map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"kid": "test-key",
			"alg": "RS256",
			"use": "sig",
			"n":   base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(publicExponent),
		}},
	}
	data, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
}

func signTestToken(t *testing.T, key *rsa.PrivateKey, issuer, audience string, notBefore int64) string {
	t.Helper()
	header := []byte(`{"alg":"RS256","kid":"test-key","typ":"JWT"}`)
	claims, err := json.Marshal(map[string]any{
		"iss":          issuer,
		"sub":          "subject-test",
		"aud":          audience,
		"exp":          time.Now().Add(time.Hour).Unix(),
		"iat":          time.Now().Add(-time.Minute).Unix(),
		"nbf":          notBefore,
		"provider_id":  "provider-test",
		"realm_access": map[string]any{"roles": []string{RoleProvider}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	input := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + encode(signature)
}

func unsignedTestToken(kid string) string {
	encode := base64.RawURLEncoding.EncodeToString
	header := encode([]byte(`{"alg":"RS256","kid":"` + kid + `"}`))
	claims := encode([]byte(`{"iss":"https://issuer.example/realms/wagering","sub":"subject","aud":"wagering-api","exp":4102444800,"iat":1700000000}`))
	return strings.Join([]string{header, claims, encode([]byte("not-a-signature"))}, ".")
}
