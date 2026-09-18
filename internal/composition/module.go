// Package composition wires the process together with Uber Fx: configuration,
// PostgreSQL, SQS, the OIDC verifier, the application use cases and the HTTP
// adapter, plus the lifecycle ordering of their shutdown.
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
	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/outbox"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/query"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/config"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/keycloak"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
	sqsinfrastructure "github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/sqs"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/observability"
	transporthttp "github.com/leonardodacosta/distributedBettingProcessing/internal/transport/http"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/transport/messaging"
)

// Module is the single Fx module of the API process.
func Module() fx.Option {
	return fx.Options(
		fx.Module("api",
			fx.Provide(
				config.Load,
				newLogger,
				observability.NewMetrics,
				newDatabase,
				newSQSClient,
				newAuthenticator,
				// The financial and read services are exposed to the transport only
				// through their use case interfaces, keeping the adapter free of
				// application implementation details.
				fx.Annotate(
					newFinancialService,
					fx.As(new(transporthttp.FinancialUseCases)),
					fx.As(new(messaging.Processor)),
				),
				fx.Annotate(query.NewService, fx.As(new(transporthttp.QueryUseCases))),
				fx.Annotate(
					postgres.NewPostgresChecker,
					fx.As(new(transporthttp.Checker)),
					fx.ResultTags(`group:"health_checkers"`),
				),
				fx.Annotate(
					newSQSChecker,
					fx.As(new(transporthttp.Checker)),
					fx.ResultTags(`group:"health_checkers"`),
				),
				fx.Annotate(
					newHealthRegistry,
					fx.ParamTags(`group:"health_checkers"`),
				),
				newSQSConsumer,
				newReferenceWorker,
				newOutboxWorker,
				newRouter,
				newHTTPServer,
			),
			fx.Invoke(registerLifecycle),
		),
		// Application logs use slog's JSON handler. Fx lifecycle diagnostics are
		// intentionally suppressed so the operational stream remains one JSON
		// record per line; this does not alter lifecycle hooks.
		fx.NopLogger,
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

func newSQSClient(cfg config.Config) (*sqsinfrastructure.Client, error) {
	return sqsinfrastructure.NewClient(context.Background(), cfg)
}

func newSQSChecker(client *sqsinfrastructure.Client) transporthttp.Checker { return client }

// newAuthenticator builds the OIDC verifier. Issuer and audience validation are
// never relaxed; the JWKS endpoint is the configured internal URL.
func newAuthenticator(cfg config.Config) (transporthttp.Authenticator, error) {
	return keycloak.NewVerifier(context.Background(), cfg.OIDCIssuer, cfg.OIDCJWKSURL, cfg.OAuthAudience)
}

// newHealthRegistry collects every readiness check registered in the
// health_checkers group. PostgreSQL and SQS are both required dependencies.
func newHealthRegistry(checkers []transporthttp.Checker) *transporthttp.HealthRegistry {
	return transporthttp.NewHealthRegistry(checkers...)
}

func newFinancialService(db *postgres.Repository, metrics *observability.Metrics) *financial.Service {
	return financial.NewService(db, metrics)
}

func newSQSConsumer(client *sqsinfrastructure.Client, processor messaging.Processor, cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) *messaging.Consumer {
	return messaging.NewConsumer(client, processor, messaging.Config{
		ConsumerName:           "wager-transaction-consumer",
		VisibilityTimeout:      cfg.SQSVisibilityTimeout,
		WaitTime:               cfg.SQSWaitTime,
		MaxMessages:            cfg.SQSMaxMessages,
		MaxReceiveCount:        cfg.SQSMaxReceiveCount,
		RetryVisibilityBackoff: cfg.SQSRetryVisibilityBackoff,
		Logger:                 logger,
		Metrics:                metrics,
	})
}

func newReferenceWorker(db *postgres.Repository, cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) *financial.ReferenceWorker {
	return financial.NewReferenceWorker(financial.NewService(db, metrics), financial.ReferenceWorkerConfig{
		PollInterval: cfg.ReferencePollInterval,
		MaxAttempts:  cfg.ReferenceMaxAttempts,
		Backoff:      cfg.ReferenceBackoff,
		Logger:       logger,
		Metrics:      metrics,
	})
}

func newOutboxWorker(db *postgres.Repository, client *sqsinfrastructure.Client, cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) *outbox.Worker {
	if !cfg.OutboxEnabled {
		return nil
	}
	return outbox.NewWorker(db, client, outbox.Config{
		PollInterval: cfg.OutboxPollInterval,
		BatchSize:    cfg.OutboxBatchSize,
		MaxAttempts:  cfg.OutboxMaxAttempts,
		Backoff:      cfg.OutboxBackoff,
		ClaimLease:   cfg.OutboxClaimLease,
		Logger:       logger,
		Metrics:      metrics,
	})
}

func newHTTPServer(cfg config.Config, router *transporthttp.Router, logger *slog.Logger) *transporthttp.Server {
	return transporthttp.NewServer(cfg.HTTPAddr, router.Handler(), logger)
}

func newRouter(financial transporthttp.FinancialUseCases, queries transporthttp.QueryUseCases, health *transporthttp.HealthRegistry, authenticator transporthttp.Authenticator, logger *slog.Logger, metrics *observability.Metrics) *transporthttp.Router {
	return transporthttp.NewRouter(financial, queries, health, authenticator, logger, metrics)
}

// registerLifecycle defines the shutdown order explicitly.
//
// Fx runs OnStop hooks in reverse registration order, so the dependency hook is
// registered first and the HTTP hook second. Shutdown therefore drains in-flight
// requests before the pool is closed:
//
//  1. stop the SQS consumer and release in-flight messages;
//  2. stop accepting new connections;
//  3. let in-flight requests finish within the configured budget;
//  4. if the budget expires, cancel the remaining work through its context and
//     close the connections;
//  5. close the database pool.
func registerLifecycle(lifecycle fx.Lifecycle, db *postgres.Repository, server *transporthttp.Server, consumer *messaging.Consumer, referenceWorker *financial.ReferenceWorker, outboxWorker *outbox.Worker, cfg config.Config, logger *slog.Logger) {
	consumers := []lifecycleConsumer{consumer, referenceWorker}
	if outboxWorker != nil {
		consumers = append(consumers, outboxWorker)
	}
	appendLifecycleHooks(lifecycle, db, server, cfg, logger, consumers...)
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

type lifecycleConsumer interface {
	Start(context.Context) error
	Stop(context.Context) error
}

func appendLifecycleHooks(lifecycle fx.Lifecycle, db lifecycleDatabase, server lifecycleServer, cfg config.Config, logger *slog.Logger, consumers ...lifecycleConsumer) {
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
	for _, consumer := range consumers {
		consumer := consumer
		lifecycle.Append(fx.Hook{
			OnStart: func(ctx context.Context) error {
				return consumer.Start(ctx)
			},
			OnStop: func(ctx context.Context) error {
				return consumer.Stop(ctx)
			},
		})
	}
}
