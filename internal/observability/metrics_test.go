package observability

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMetricsRenderStableLowCardinalitySnapshot(t *testing.T) {
	metrics := NewMetrics()
	metrics.ObserveProcessing("PROCESSED", 2*time.Second)
	metrics.ObserveDuplicate(DuplicateKindSQSInbox)
	metrics.ObserveRetry("sqs")
	metrics.ObserveRedriveCandidate()
	metrics.ObserveIdempotencyConflict()
	metrics.ObserveReconciliationDivergence()
	metrics.SetOutboxLag(1500 * time.Millisecond)
	output := metrics.Render()
	for _, want := range []string{
		`wager_processing_total{status="PROCESSED"} 1`,
		`wager_duplicate_total{kind="sqs_inbox"} 1`,
		`wager_retry_total{component="sqs"} 1`,
		"wager_sqs_redrive_candidate_total 1",
		"wager_idempotency_conflict_total 1",
		"wager_outbox_lag_seconds 1.500000000",
		"wager_reconciliation_divergence_total 1",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("metrics missing %q in %s", want, output)
		}
	}
	if strings.Contains(output, "transactionId") || strings.Contains(output, "providerId") {
		t.Fatal("business identifiers leaked into metrics")
	}
}

func TestNormalizeCorrelationID(t *testing.T) {
	valid := "trace-01.A_b:ok"
	if got := NormalizeCorrelationID(valid); got != valid {
		t.Fatalf("valid correlation ID = %q, want %q", got, valid)
	}
	boundary := strings.Repeat("a", MaxCorrelationIDLength)
	if got := NormalizeCorrelationID(boundary); got != boundary {
		t.Fatalf("boundary correlation ID was replaced: %q", got)
	}
	for _, invalid := range []string{strings.Repeat("a", MaxCorrelationIDLength+1), "line\nbreak", "tab\tvalue", "quoted value", "ümlaut"} {
		if got := NormalizeCorrelationID(invalid); got == invalid {
			t.Fatalf("invalid correlation ID was preserved: %q", invalid)
		}
	}
}

func TestCorrelationContext(t *testing.T) {
	ctx := WithCorrelation(context.Background(), "correlation-1")
	if got := Correlation(ctx); got != "correlation-1" {
		t.Fatalf("correlation = %q", got)
	}
	if got := Correlation(context.Background()); got != "" {
		t.Fatalf("unexpected correlation = %q", got)
	}
}
