package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/keycloak"
)

// Authenticator validates a bearer token and derives the authenticated identity.
// It is satisfied by the Keycloak OIDC verifier; transport tests provide fakes.
type Authenticator interface {
	Verify(ctx context.Context, rawToken string) (keycloak.Identity, error)
}

type identityContextKey struct{}

func principalFrom(ctx context.Context) (keycloak.Identity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(keycloak.Identity)
	return identity, ok
}

// authenticate rejects any request without a valid token issued by the IdP.
func (r *Router) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		raw, err := bearerToken(request)
		if err != nil {
			r.writeError(w, request, err)
			return
		}
		identity, err := r.authenticator.Verify(request.Context(), raw)
		if err != nil {
			r.writeError(w, request, err)
			return
		}
		next.ServeHTTP(w, request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity)))
	})
}

// requireProvider enforces the provider role and requires the provider identity
// claim to be present. Role and provider identity are separate authorities: a
// token carrying provider_id does not grant the internal role, and a token with
// the internal role cannot act as a provider.
func (r *Router) requireProvider(next http.Handler) http.Handler {
	return r.authenticate(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		identity, ok := principalFrom(request.Context())
		if !ok || !identity.HasRole(keycloak.RoleProvider) {
			r.writeError(w, request, errForbidden)
			return
		}
		if identity.ProviderID == "" {
			r.writeError(w, request, errProviderIdentity)
			return
		}
		next.ServeHTTP(w, request)
	}))
}

// requireInternal enforces the internal role used by wallet administration.
func (r *Router) requireInternal(next http.Handler) http.Handler {
	return r.authenticate(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		identity, ok := principalFrom(request.Context())
		if !ok || !identity.HasRole(keycloak.RoleInternal) {
			r.writeError(w, request, errForbidden)
			return
		}
		next.ServeHTTP(w, request)
	}))
}

func bearerToken(request *http.Request) (string, error) {
	const prefix = "bearer "
	header := request.Header.Get("Authorization")
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", errUnauthenticated
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", errUnauthenticated
	}
	return token, nil
}

// providerIdentity returns the authenticated provider identity established by
// the provider role and the provider_id claim. Path and body values never
// substitute it.
func providerIdentity(request *http.Request) (string, error) {
	identity, ok := principalFrom(request.Context())
	if !ok || identity.ProviderID == "" {
		return "", errProviderIdentity
	}
	return identity.ProviderID, nil
}
