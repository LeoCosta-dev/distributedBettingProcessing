package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Router translates HTTP requests into application commands and application
// results back into HTTP responses. It owns no financial rule: money parsing,
// transaction semantics, idempotency and reference resolution all live behind
// the application boundary it calls, which is shared with every other
// transport.
type Router struct {
	financial     FinancialUseCases
	queries       QueryUseCases
	health        *HealthRegistry
	authenticator Authenticator
	logger        *slog.Logger
}

func NewRouter(
	financialService FinancialUseCases,
	queries QueryUseCases,
	health *HealthRegistry,
	authenticator Authenticator,
	logger *slog.Logger,
) *Router {
	return &Router{
		financial:     financialService,
		queries:       queries,
		health:        health,
		authenticator: authenticator,
		logger:        logger,
	}
}

// Handler builds the routing table.
//
// Provider routes are scoped to the authenticated provider identity; wallet
// administration routes require the internal role; health routes are public and
// therefore never reach the authenticator.
func (r *Router) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("/health/live", r.methods(map[string]http.Handler{
		http.MethodGet: http.HandlerFunc(r.liveness),
	}))
	mux.Handle("/health/ready", r.methods(map[string]http.Handler{
		http.MethodGet: http.HandlerFunc(r.readiness),
	}))

	mux.Handle("/wagering/transactions", r.methods(map[string]http.Handler{
		http.MethodPost: r.requireProvider(http.HandlerFunc(r.processTransaction)),
	}))
	mux.Handle("/wagering/transactions/{transactionId}", r.methods(map[string]http.Handler{
		http.MethodGet: r.requireProvider(http.HandlerFunc(r.transactionByID)),
	}))
	mux.Handle("/providers/{providerId}/wagering/transactions/{externalTransactionId}", r.methods(map[string]http.Handler{
		http.MethodGet: r.requireProvider(http.HandlerFunc(r.transactionByExternalID)),
	}))

	mux.Handle("/wallets", r.methods(map[string]http.Handler{
		http.MethodPost: r.requireInternal(http.HandlerFunc(r.openWallet)),
	}))
	mux.Handle("/wallets/{walletId}", r.methods(map[string]http.Handler{
		http.MethodGet: r.requireInternal(http.HandlerFunc(r.wallet)),
	}))
	mux.Handle("/wallets/{walletId}/ledger", r.methods(map[string]http.Handler{
		http.MethodGet: r.requireInternal(http.HandlerFunc(r.walletLedger)),
	}))
	mux.Handle("/wallets/{walletId}/reconciliation", r.methods(map[string]http.Handler{
		http.MethodPost: r.requireInternal(http.HandlerFunc(r.reconcileWallet)),
	}))

	// Unknown paths answer with the uniform error envelope instead of the
	// plain text default of net/http.
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		r.writeError(w, request, errRouteNotFound)
	}))
	return mux
}

// methods dispatches on the request method and answers a wrong method with the
// uniform envelope plus the Allow header.
func (r *Router) methods(routes map[string]http.Handler) http.Handler {
	allowed := make([]string, 0, len(routes))
	for method := range routes {
		allowed = append(allowed, method)
	}
	sort.Strings(allowed)
	allowHeader := strings.Join(allowed, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		handler, ok := routes[request.Method]
		if !ok {
			w.Header().Set("Allow", allowHeader)
			r.writeError(w, request, errMethodNotAllowed)
			return
		}
		handler.ServeHTTP(w, request)
	})
}

func (r *Router) writeError(w http.ResponseWriter, request *http.Request, err error) {
	writeError(w, request, r.logger, err)
}

// Server owns the listening socket and the HTTP server lifecycle.
type Server struct {
	httpServer *http.Server
	listener   net.Listener
	logger     *slog.Logger
	cancel     context.CancelFunc
}

func NewServer(addr string, handler http.Handler, logger *slog.Logger) *Server {
	// Every request context derives from this one, so a forced close cancels
	// the work still in flight instead of leaving it running unobserved.
	base, cancel := context.WithCancel(context.Background())
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return base },
	}
	return &Server{httpServer: httpServer, logger: logger, cancel: cancel}
}

// Listen binds the configured address synchronously, so a startup failure (for
// example a busy port) fails the application lifecycle instead of being
// discovered inside a goroutine.
func (s *Server) Listen() error {
	listener, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("http server listen on %s: %w", s.httpServer.Addr, err)
	}
	s.listener = listener
	return nil
}

// Addr reports the bound address. It is empty when Listen was not called.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Serve accepts connections until Shutdown or Close is called.
func (s *Server) Serve() {
	if s.listener == nil {
		return
	}
	go func() {
		if err := s.httpServer.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if s.logger != nil {
				s.logger.Error("http server stopped unexpectedly", slog.String("error", err.Error()))
			}
		}
	}()
}

// Shutdown stops accepting new connections and waits for the requests already
// in flight until the context expires.
func (s *Server) Shutdown(ctx context.Context) error { return s.httpServer.Shutdown(ctx) }

// Close stops the server immediately: the base context is cancelled, so the
// requests still in flight observe the cancellation, and the open connections
// are closed. It is the recovery path once the drain budget is exhausted.
func (s *Server) Close() error {
	s.cancel()
	return s.httpServer.Close()
}
