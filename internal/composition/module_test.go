package composition

import (
	"context"
	"os"
	"testing"
	"time"

	"go.uber.org/fx"
)

func TestModuleStartsAndStopsWithPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	t.Setenv("DATABASE_URL", databaseURL)
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("OIDC_ISSUER", "http://localhost:8082/realms/wagering")
	t.Setenv("OIDC_JWKS_URL", "http://localhost:8082/realms/wagering/protocol/openid-connect/certs")
	t.Setenv("OAUTH_AUDIENCE", "wagering-api")
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "2s")
	t.Setenv("LOG_LEVEL", "error")

	app := fx.New(Module(), fx.NopLogger)
	startContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Start(startContext); err != nil {
		t.Fatal(err)
	}
	stopContext, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := app.Stop(stopContext); err != nil {
		t.Fatal(err)
	}
}
