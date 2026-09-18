// Package observability contains process-local diagnostic instruments. The
// instruments are deliberately low-cardinality: business identifiers are
// recorded in structured logs, never as metric labels.
package observability

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type contextKey struct{}

// Metrics is safe for concurrent use by HTTP handlers and background workers.
type Metrics struct {
	mu          sync.Mutex
	status      map[string]uint64
	retries     map[string]uint64
	duplicates  map[string]uint64
	latencyN    atomic.Uint64
	latencyNS   atomic.Uint64
	redrive     atomic.Uint64
	idempotency atomic.Uint64
	conflicts   atomic.Uint64
	divergence  atomic.Uint64
	outboxLagNS atomic.Int64
}

func NewMetrics() *Metrics {
	return &Metrics{status: make(map[string]uint64), retries: make(map[string]uint64), duplicates: make(map[string]uint64)}
}

func (m *Metrics) ObserveProcessing(status string, duration time.Duration) {
	if m == nil {
		return
	}
	if status == "" {
		status = "error"
	}
	m.mu.Lock()
	m.status[status]++
	m.mu.Unlock()
	m.latencyN.Add(1)
	if duration > 0 {
		m.latencyNS.Add(uint64(duration))
	}
}

const (
	DuplicateKindSQSInbox        = "sqs_inbox"
	DuplicateKindHTTPIdempotency = "http_idempotency"
)

func (m *Metrics) ObserveDuplicate(kind string) {
	if m == nil {
		return
	}
	switch kind {
	case DuplicateKindSQSInbox, DuplicateKindHTTPIdempotency:
	default:
		kind = "unknown"
	}
	m.mu.Lock()
	m.duplicates[kind]++
	m.mu.Unlock()
}
func (m *Metrics) ObserveRetry(component string) {
	if m == nil {
		return
	}
	switch component {
	case "sqs", "pending_reference", "outbox":
	default:
		component = "unknown"
	}
	m.mu.Lock()
	m.retries[component]++
	m.mu.Unlock()
}

// ObserveRedriveCandidate records a message that exhausted the application
// receive budget and is now a candidate for broker redrive. The application
// does not observe the broker's subsequent DLQ insertion directly.
func (m *Metrics) ObserveRedriveCandidate() {
	if m != nil {
		m.redrive.Add(1)
	}
}
func (m *Metrics) ObserveIdempotencyConflict() {
	if m != nil {
		m.idempotency.Add(1)
	}
}
func (m *Metrics) ObserveConcurrencyConflict() {
	if m != nil {
		m.conflicts.Add(1)
	}
}
func (m *Metrics) ObserveReconciliationDivergence() {
	if m != nil {
		m.divergence.Add(1)
	}
}
func (m *Metrics) SetOutboxLag(d time.Duration) {
	if m != nil {
		m.outboxLagNS.Store(int64(d))
	}
}

// WithCorrelation stores a request/message correlation identifier in context.
func WithCorrelation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}
func Correlation(ctx context.Context) string {
	if id, ok := ctx.Value(contextKey{}).(string); ok {
		return id
	}
	return ""
}
func NewCorrelationID() string { return uuid.NewString() }

const MaxCorrelationIDLength = 128

// NormalizeCorrelationID applies the implementation policy for the
// untrusted HTTP correlation header. Printable ASCII identifiers with a
// bounded length are preserved; empty, oversized, whitespace-surrounded or
// control/non-ASCII input is replaced with a fresh identifier.
func NormalizeCorrelationID(raw string) string {
	id := raw
	if id == "" || len(id) > MaxCorrelationIDLength {
		return NewCorrelationID()
	}
	for _, char := range id {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return NewCorrelationID()
	}
	return id
}

// Render returns a stable Prometheus text exposition. It intentionally omits
// financial payloads and identifiers.
func (m *Metrics) Render() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	statuses := make(map[string]uint64, len(m.status))
	for k, v := range m.status {
		statuses[k] = v
	}
	retries := make(map[string]uint64, len(m.retries))
	for k, v := range m.retries {
		retries[k] = v
	}
	duplicates := make(map[string]uint64, len(m.duplicates))
	for k, v := range m.duplicates {
		duplicates[k] = v
	}
	m.mu.Unlock()
	var b strings.Builder
	b.WriteString("# TYPE wager_processing_total counter\n")
	keys := sortedKeys(statuses)
	for _, key := range keys {
		fmt.Fprintf(&b, "wager_processing_total{status=%q} %d\n", key, statuses[key])
	}
	b.WriteString("# TYPE wager_duplicate_total counter\n")
	for _, key := range sortedKeys(duplicates) {
		fmt.Fprintf(&b, "wager_duplicate_total{kind=%q} %d\n", key, duplicates[key])
	}
	b.WriteString("# TYPE wager_retry_total counter\n")
	for _, key := range sortedKeys(retries) {
		fmt.Fprintf(&b, "wager_retry_total{component=%q} %d\n", key, retries[key])
	}
	b.WriteString("# HELP wager_sqs_redrive_candidate_total Messages that exhausted the application receive budget and became candidates for broker redrive.\n")
	b.WriteString("# TYPE wager_sqs_redrive_candidate_total counter\n")
	fmt.Fprintf(&b, "wager_sqs_redrive_candidate_total %d\n", m.redrive.Load())
	b.WriteString("# TYPE wager_idempotency_conflict_total counter\n")
	fmt.Fprintf(&b, "wager_idempotency_conflict_total %d\n", m.idempotency.Load())
	b.WriteString("# TYPE wager_concurrency_conflict_total counter\n")
	fmt.Fprintf(&b, "wager_concurrency_conflict_total %d\n", m.conflicts.Load())
	b.WriteString("# TYPE wager_processing_latency_seconds summary\n")
	fmt.Fprintf(&b, "wager_processing_latency_seconds_count %d\n", m.latencyN.Load())
	fmt.Fprintf(&b, "wager_processing_latency_seconds_sum %s\n", strconv.FormatFloat(float64(m.latencyNS.Load())/float64(time.Second), 'f', 9, 64))
	b.WriteString("# TYPE wager_outbox_lag_seconds gauge\n")
	fmt.Fprintf(&b, "wager_outbox_lag_seconds %s\n", strconv.FormatFloat(float64(m.outboxLagNS.Load())/float64(time.Second), 'f', 9, 64))
	b.WriteString("# TYPE wager_reconciliation_divergence_total counter\n")
	fmt.Fprintf(&b, "wager_reconciliation_divergence_total %d\n", m.divergence.Load())
	return b.String()
}

func sortedKeys(values map[string]uint64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
