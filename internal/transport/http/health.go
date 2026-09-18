package http

import (
	"context"
	"net/http"
	"time"
)

// checkTimeout bounds a single readiness probe.
const checkTimeout = 2 * time.Second

// Checker is one readiness dependency. Additional dependencies (for example the
// SQS connectivity added in a later loop) only need to implement this contract
// and be provided to NewHealthRegistry; no handler changes are required.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// HealthRegistry aggregates the readiness checks in a stable order.
type HealthRegistry struct {
	checkers []Checker
}

func NewHealthRegistry(checkers ...Checker) *HealthRegistry {
	return &HealthRegistry{checkers: checkers}
}

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// liveness reports that the process is alive. It checks no dependency.
func (r *Router) liveness(w http.ResponseWriter, request *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "UP"})
}

// readiness reports the state of the required dependencies. A dependency outage
// fails readiness without terminating the process.
func (r *Router) readiness(w http.ResponseWriter, request *http.Request) {
	checks := make(map[string]string, len(r.health.checkers))
	ready := true
	for _, checker := range r.health.checkers {
		ctx, cancel := context.WithTimeout(request.Context(), checkTimeout)
		err := checker.Check(ctx)
		cancel()
		if err != nil {
			checks[checker.Name()] = "DOWN"
			ready = false
			if r.logger != nil {
				r.logger.Warn("readiness check failed", "check", checker.Name())
			}
			continue
		}
		checks[checker.Name()] = "UP"
	}
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "DOWN", Checks: checks})
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: "UP", Checks: checks})
}
