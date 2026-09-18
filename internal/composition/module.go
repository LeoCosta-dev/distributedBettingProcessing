// Package composition wires the process together with Uber Fx: configuration,
// PostgreSQL, the OIDC verifier, the application use cases and the HTTP adapter,
// plus the lifecycle ordering of their shutdown.
//
// The package lives outside internal/fx on purpose so that the Fx import never
// shares a name with the package it composes. The domain and the application
// layers do not import Fx; only this package does.
package composition

import (
	"context"
	"log/slog"
	"os"

	"go.uber.org/fx"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/query"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/config"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/keycloak"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
	transporthttp "github.com/leonardodacosta/distributedBettingProcessing/internal/transport/http"
)

// Module is the single Fx module of the API process.
func Module() fx.Option {
	return fx.Module("api",
		fx.Provide(
			config.Load,
			newLogger,
			newDatabase,
			newAuthenticator,
			// The financial and read services are exposed to the transport only
			// through their use case interfaces, keeping the adapter free of
			// application implementation details.
			fx.Annotate(financial.NewService, fx.As(new(transporthttp.FinancialUseCases))),
			fx.Annotate(query.NewService, fx.As(new(transporthttp.QueryUseCases))),
			fx.Annotate(
				postgres.NewPostgresChecker,
				fx.As(new(transporthttp.Checker)),
				fx.ResultTags(`group:"health_checkers"`),
			),
			fx.Annotate(
				newHealthRegistry,
				fx.ParamTags(`group:"health_checkers"`),
			),
			transporthttp.NewRouter,
			newHTTPServer,
		),
		fx.Invoke(registerLifecycle),
	)
}

// newLogger builds the structured logger. Only lifecycle and error events are
// emitted; financial payloads and credentials are never logged.
func newLogger(cfg config.Config) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.Level(cfg.LogLevelValue())})
	return slog.New(handler)
}

// newDatabase opens the connection pool. The pool context only scopes pool
// creation; closing the pool belongs to the lifecycle ordering below.
func newDatabase(cfg config.Config) (*postgres.Repository, error) {
	return postgres.NewRepository(context.Background(), cfg.DatabaseURL)
}

// newAuthenticator builds the OIDC verifier. Issuer and audience validation are
// never relaxed; the JWKS endpoint is the configured internal URL.
func newAuthenticator(cfg config.Config) (transporthttp.Authenticator, error) {
	return keycloak.NewVerifier(context.Background(), cfg.OIDCIssuer, cfg.OIDCJWKSURL, cfg.OAuthAudience)
}

// newHealthRegistry collects every readiness check registered in the
// health_checkers group. Adding the SQS check in a later loop means registering
// one more checker; no handler or wiring change is required.
func newHealthRegistry(checkers []transporthttp.Checker) *transporthttp.HealthRegistry {
	return transporthttp.NewHealthRegistry(checkers...)
}

func newHTTPServer(cfg config.Config, router *transporthttp.Router, logger *slog.Logger) *transporthttp.Server {
	return transporthttp.NewServer(cfg.HTTPAddr, router.Handler(), logger)
}

// registerLifecycle defines the shutdown order explicitly.
//
// Fx runs OnStop hooks in reverse registration order, so the dependency hook is
// registered first and the HTTP hook second. Shutdown therefore drains in-flight
// requests before the pool is closed:
//
//  1. stop accepting new connections;
//  2. let in-flight requests finish within the configured budget;
//  3. if the budget expires, cancel the remaining work through its context and
//     close the connections;
//  4. close the remaining dependencies.
func registerLifecycle(lifecycle fx.Lifecycle, db *postgres.Repository, server *transporthttp.Server, cfg config.Config, logger *slog.Logger) {
	appendLifecycleHooks(lifecycle, db, server, cfg, logger)
}

type lifecycleDatabase interface {
	Close()
}

type lifecycleServer interface {
	Listen() error
	Addr() string
	Serve()
	Shutdown(context.Context) error
	Close() error
}

func appendLifecycleHooks(lifecycle fx.Lifecycle, db lifecycleDatabase, server lifecycleServer, cfg config.Config, logger *slog.Logger) {
	lifecycle.Append(fx.Hook{
		OnStop: func(context.Context) error {
			db.Close()
			logger.Info("database pool closed")
			return nil
		},
	})
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			if err := server.Listen(); err != nil {
				return err
			}
			server.Serve()
			logger.Info("http server listening", slog.String("addr", server.Addr()))
			return nil
		},
		OnStop: func(ctx context.Context) error {
			drainCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
			defer cancel()
			logger.Info("http server draining", slog.String("timeout", cfg.ShutdownTimeoutSeconds()))
			if err := server.Shutdown(drainCtx); err != nil {
				logger.Warn("drain budget exhausted, closing remaining work",
					slog.String("error", err.Error()))
				_ = server.Close()
			}
			logger.Info("http server stopped")
			return nil
		},
	})
}
