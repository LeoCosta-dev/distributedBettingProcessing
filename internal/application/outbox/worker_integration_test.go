package outbox

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/observability"
)

type recordingPublisher struct {
	mu       sync.Mutex
	calls    []string
	failures int
	target   string
}

type holdingPublisher struct {
	target       string
	started      chan struct{}
	startedEvent chan string
	release      chan struct{}
	once         sync.Once
	releaseOnce  sync.Once
	recorder     *recordingPublisher
}

func (p *holdingPublisher) releaseWait() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func (p *holdingPublisher) SendEvent(ctx context.Context, body, groupID, eventID string) error {
	if p.target == "" || eventID == p.target {
		p.once.Do(func() { close(p.started) })
		if p.startedEvent != nil {
			p.startedEvent <- eventID
		}
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if p.recorder != nil {
		return p.recorder.SendEvent(ctx, body, groupID, eventID)
	}
	return nil
}

func (p *recordingPublisher) SendEvent(_ context.Context, body, _, eventID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, eventID+"|"+body)
	if p.eventID(eventID) && p.failures > 0 {
		p.failures--
		return errors.New("temporary publisher failure")
	}
	return nil
}

func (p *recordingPublisher) eventID(eventID string) bool {
	return p.target == "" || p.target == eventID
}

func (p *recordingPublisher) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func TestWorkerPublishesStableEnvelopeAndMarksPublished(t *testing.T) {
	db := newOutboxTestDB(t)
	defer db.Close()
	eventID := insertOutboxTestEvent(t, db, time.Now().UTC())
	publisher := &recordingPublisher{target: eventID.String()}
	worker := NewWorker(db, publisher, Config{Backoff: time.Second, MaxAttempts: 3, ClaimLease: time.Minute})
	if err := processUntilPublished(context.Background(), worker, db, eventID); err != nil {
		t.Fatal(err)
	}
	record, err := postgres.NewOutboxRepository(db).FindByID(context.Background(), eventID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "PUBLISHED" || record.Attempts != 1 || record.PublishedAt == nil || record.ClaimToken != uuid.Nil {
		t.Fatalf("published record = %+v", record)
	}
	calls := publisher.snapshot()
	if len(targetCalls(calls, eventID)) != 1 || !contains(targetCalls(calls, eventID)[0], `"eventType":"TestEvent"`) || !contains(targetCalls(calls, eventID)[0], `"data":{"ok":true}`) {
		t.Fatalf("published envelope = %v", calls)
	}
}

func TestWorkerRetriesWithStableEventIDAndRecoversAbandonedClaim(t *testing.T) {
	db := newOutboxTestDB(t)
	defer db.Close()
	eventID := insertOutboxTestEvent(t, db, time.Now().UTC())
	publisher := &recordingPublisher{failures: 1, target: eventID.String()}
	worker := NewWorker(db, publisher, Config{Backoff: 0, MaxAttempts: 3, ClaimLease: time.Minute})
	repository := postgres.NewOutboxRepository(db)
	record, err := repository.ClaimRecordByID(context.Background(), eventID, time.Now().UTC(), time.Minute)
	if err != nil || record.EventID != eventID {
		t.Fatalf("claim target = %+v error=%v", record, err)
	}
	body, err := marshalEnvelope(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.SendEvent(context.Background(), body, record.AggregateID.String(), record.EventID.String()); err == nil {
		t.Fatal("publisher unexpectedly succeeded")
	}
	if err := repository.MarkRetry(context.Background(), record.EventID, record.ClaimToken, record.Attempts, time.Unix(0, 0).UTC(), "temporary publisher failure", 3, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	record, err = repository.FindByID(context.Background(), eventID)
	if err != nil || record.Status != "PENDING" || record.Attempts != 1 {
		t.Fatalf("retry record = %+v error=%v", record, err)
	}
	if _, err := repository.ClaimRecordByID(context.Background(), eventID, time.Now().UTC().Add(-2*time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := processUntilPublished(context.Background(), worker, db, eventID); err != nil {
		t.Fatal(err)
	}
	record, err = postgres.NewOutboxRepository(db).FindByID(context.Background(), eventID)
	if err != nil || record.Status != "PUBLISHED" || record.Attempts != 3 {
		t.Fatalf("recovered record = %+v error=%v", record, err)
	}
	calls := publisher.snapshot()
	targetCalls := targetCalls(calls, eventID)
	if len(targetCalls) != 2 || !contains(targetCalls[0], eventID.String()) || !contains(targetCalls[1], eventID.String()) {
		t.Fatalf("event id was not stable across retry: %v", calls)
	}
}

func TestTwoOutboxWorkersClaimOneRecordAcrossIndependentPools(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	dbA, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	eventID := insertOutboxTestEvent(t, dbA, time.Now().UTC())
	publisher := &recordingPublisher{target: eventID.String()}
	workerA := NewWorker(dbA, publisher, Config{ClaimLease: time.Minute})
	workerB := NewWorker(dbB, publisher, Config{ClaimLease: time.Minute})
	results := make(chan error, 2)
	go func() { results <- processUntilPublished(ctx, workerA, dbA, eventID) }()
	go func() { results <- processUntilPublished(ctx, workerB, dbB, eventID) }()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	record, err := postgres.NewOutboxRepository(dbA).FindByID(ctx, eventID)
	if err != nil || record.Status != "PUBLISHED" || record.Attempts != 1 {
		t.Fatalf("claimed record = %+v error=%v", record, err)
	}
	if calls := targetCalls(publisher.snapshot(), eventID); len(calls) != 1 {
		t.Fatalf("publisher calls for target = %d, want 1", len(calls))
	}
}

func TestOutboxOrderingBlocksSameAggregateAndAllowsIndependentAggregate(t *testing.T) {
	db := newOutboxTestDB(t)
	defer db.Close()
	ctx := context.Background()
	repository := postgres.NewOutboxRepository(db)
	aggregateA, aggregateB := uuid.New(), uuid.New()
	e1 := insertOutboxTestEventForAggregate(t, db, aggregateA, time.Now().UTC())
	e2 := insertOutboxTestEventForAggregate(t, db, aggregateA, time.Now().UTC())
	eB := insertOutboxTestEventForAggregate(t, db, aggregateB, time.Now().UTC())
	claimedE1, err := repository.ClaimRecordByID(ctx, e1, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ClaimRecordByID(ctx, e2, time.Now().UTC(), time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("later same-aggregate event claim = %v, want blocked", err)
	}
	claimedB, err := repository.ClaimRecordByID(ctx, eB, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("independent aggregate claim: %v", err)
	}
	if err := repository.MarkPublished(ctx, eB, claimedB.ClaimToken, time.Now().UTC()); err != nil {
		t.Fatalf("independent aggregate publish: %v", err)
	}
	if err := repository.MarkRetry(ctx, claimedE1.EventID, claimedE1.ClaimToken, claimedE1.Attempts, time.Unix(0, 0).UTC(), "temporary", 3, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ClaimRecordByID(ctx, e2, time.Now().UTC(), time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("E2 bypassed retryable predecessor: %v", err)
	}
	retriedE1, err := repository.ClaimRecordByID(ctx, e1, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkPublished(ctx, e1, retriedE1.ClaimToken, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ClaimRecordByID(ctx, e2, time.Now().UTC(), time.Minute); err != nil {
		t.Fatalf("E2 did not become eligible after E1: %v", err)
	}
	aggregateC := uuid.New()
	e3 := insertOutboxTestEventForAggregate(t, db, aggregateC, time.Now().UTC())
	e4 := insertOutboxTestEventForAggregate(t, db, aggregateC, time.Now().UTC())
	claimedE3, err := repository.ClaimRecordByID(ctx, e3, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkRetry(ctx, e3, claimedE3.ClaimToken, claimedE3.Attempts, time.Now().UTC(), "permanent", 1, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ClaimRecordByID(ctx, e4, time.Now().UTC(), time.Minute); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("successor bypassed FAILED predecessor: %v", err)
	}
}

func TestStaleOutboxPublisherCannotMutateRecoveredClaim(t *testing.T) {
	db := newOutboxTestDB(t)
	defer db.Close()
	ctx := context.Background()
	eventID := insertOutboxTestEvent(t, db, time.Now().UTC())
	repository := postgres.NewOutboxRepository(db)
	first, err := repository.ClaimRecordByID(ctx, eventID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.ClaimRecordByID(ctx, eventID, time.Now().UTC().Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if first.ClaimToken == second.ClaimToken {
		t.Fatal("claim token did not change after lease recovery")
	}
	if err := repository.MarkPublished(ctx, eventID, second.ClaimToken, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkRetry(ctx, eventID, first.ClaimToken, first.Attempts, time.Now().UTC(), "stale", 3, time.Now().UTC()); !errors.Is(err, postgres.ErrOutboxClaimLost) {
		t.Fatalf("stale MarkRetry = %v, want ErrOutboxClaimLost", err)
	}
	record, err := repository.FindByID(ctx, eventID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "PUBLISHED" || record.PublishedAt == nil || record.ClaimToken != uuid.Nil {
		t.Fatalf("stale publisher changed recovered claim: %+v", record)
	}
}

func TestProductionWorkerClaimOneBlocksSameAggregateAndAllowsParallelAggregate(t *testing.T) {
	requireProductionOutboxIntegration(t)
	databaseURL := newIsolatedOutboxTestDatabaseURL(t)
	ctx := context.Background()
	dbA, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	aggregateA, aggregateB := uuid.New(), uuid.New()
	e1 := insertOutboxTestEventForAggregate(t, dbA, aggregateA, time.Now().UTC())
	e2 := insertOutboxTestEventForAggregate(t, dbA, aggregateA, time.Now().UTC())
	e3 := insertOutboxTestEventForAggregate(t, dbA, aggregateB, time.Now().UTC())
	claimTime := time.Now().UTC()
	setOutboxNextAttempt(t, ctx, databaseURL, e2, claimTime.Add(-2*time.Minute))
	setOutboxNextAttempt(t, ctx, databaseURL, e3, claimTime.Add(-time.Minute))
	setOutboxNextAttempt(t, ctx, databaseURL, e1, time.Unix(0, 0).UTC())

	recorder := &recordingPublisher{target: ""}
	holding := &holdingPublisher{started: make(chan struct{}), startedEvent: make(chan string, 1), release: make(chan struct{}), recorder: recorder}
	t.Cleanup(holding.releaseWait)
	workerA := NewWorker(dbA, holding, Config{ClaimLease: time.Minute})
	workerB := NewWorker(dbB, recorder, Config{ClaimLease: time.Minute})
	aDone := make(chan error, 1)
	go func() {
		_, processErr := workerA.processOne(ctx, time.Now().UTC())
		aDone <- processErr
	}()
	select {
	case <-holding.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker A did not claim E1 through ClaimOneRecord")
	}
	if claimedID := <-holding.startedEvent; claimedID != e1.String() {
		holding.releaseWait()
		t.Fatalf("production claim selected %s before E1", claimedID)
	}
	if record, err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e2); err != nil || record.Status != "PENDING" || record.ClaimedAt != nil || !record.NextAttemptAt.Before(claimTime) {
		t.Fatalf("E2 was not independently eligible: %+v, error=%v", record, err)
	}
	if record, err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e3); err != nil || record.Status != "PENDING" || record.ClaimedAt != nil || !record.NextAttemptAt.Before(claimTime) {
		t.Fatalf("E3 was not independently eligible: %+v, error=%v", record, err)
	}
	e2Record, e2Err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e2)
	e3Record, e3Err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e3)
	e1Record, e1Err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e1)
	if e1Err != nil || e2Err != nil || e3Err != nil || e1Record.OrderingID >= e2Record.OrderingID || e2Record.OrderingID >= e3Record.OrderingID {
		t.Fatalf("scenario ordering = E1=%+v error=%v E2=%+v error=%v E3=%+v error=%v", e1Record, e1Err, e2Record, e2Err, e3Record, e3Err)
	}
	assertOutboxRowUnlocked(t, ctx, databaseURL, e2)
	if _, err := workerB.processOne(ctx, claimTime); err != nil {
		e3Record, e3Err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e3)
		t.Fatalf("parallel aggregate claim: %v; E3=%+v lookup=%v", err, e3Record, e3Err)
	}
	if record, err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e2); err != nil || record.Status != "PENDING" {
		t.Fatalf("E2 was not blocked by claimed predecessor: %+v, error=%v", record, err)
	}
	if record, err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e3); err != nil || record.Status != "PUBLISHED" {
		t.Fatalf("independent aggregate did not progress: %+v, error=%v", record, err)
	}
	holding.releaseWait()
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	if _, err := workerB.processOne(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if record, err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e2); err != nil || record.Status != "PUBLISHED" {
		t.Fatalf("E2 was not published after E1: %+v, error=%v", record, err)
	}
}

func TestProductionWorkerClaimOneRetryKeepsSuccessorBlocked(t *testing.T) {
	requireProductionOutboxIntegration(t)
	databaseURL := newIsolatedOutboxTestDatabaseURL(t)
	ctx := context.Background()
	db, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	aggregate := uuid.New()
	e1 := insertOutboxTestEventForAggregate(t, db, aggregate, time.Now().UTC())
	e2 := insertOutboxTestEventForAggregate(t, db, aggregate, time.Now().UTC())
	e3 := insertOutboxTestEventForAggregate(t, db, uuid.New(), time.Now().UTC())
	claimTime := time.Now().UTC()
	setOutboxNextAttempt(t, ctx, databaseURL, e2, claimTime.Add(-2*time.Minute))
	setOutboxNextAttempt(t, ctx, databaseURL, e3, claimTime.Add(-time.Minute))
	setOutboxNextAttempt(t, ctx, databaseURL, e1, time.Unix(0, 0).UTC())
	publisher := &recordingPublisher{target: e1.String(), failures: 1}
	metrics := observability.NewMetrics()
	worker := NewWorker(db, publisher, Config{Backoff: time.Hour, ClaimLease: time.Minute, Metrics: metrics})
	now := claimTime
	if _, err := worker.processOne(ctx, now); err != nil {
		t.Fatal(err)
	}
	if record, findErr := postgres.NewOutboxRepository(db).FindByID(ctx, e1); findErr != nil || record.Status != "PENDING" || !record.NextAttemptAt.After(now) {
		t.Fatalf("E1 retry state = %+v, error=%v", record, findErr)
	}
	if got := metrics.Render(); !strings.Contains(got, `wager_retry_total{component="outbox"} 1`) {
		t.Fatalf("outbox retry was not observed through Worker.processOne: %s", got)
	}
	if _, err := worker.processOne(ctx, now); err != nil {
		t.Fatal(err)
	}
	if record, err := postgres.NewOutboxRepository(db).FindByID(ctx, e3); err != nil || record.Status != "PUBLISHED" {
		t.Fatalf("E3 did not progress while E1 was in retry: %+v, error=%v", record, err)
	}
	if record, err := postgres.NewOutboxRepository(db).FindByID(ctx, e2); err != nil || record.Status != "PENDING" || record.ClaimedAt != nil || !record.NextAttemptAt.Before(now) {
		t.Fatalf("E2 was not independently eligible during retry: %+v, error=%v", record, err)
	}
	e1Record, e1Err := postgres.NewOutboxRepository(db).FindByID(ctx, e1)
	e2Record, e2Err := postgres.NewOutboxRepository(db).FindByID(ctx, e2)
	e3Record, e3Err := postgres.NewOutboxRepository(db).FindByID(ctx, e3)
	if e1Err != nil || e2Err != nil || e3Err != nil || e1Record.OrderingID >= e2Record.OrderingID || e2Record.OrderingID >= e3Record.OrderingID {
		t.Fatalf("retry scenario ordering = E1=%+v error=%v E2=%+v error=%v E3=%+v error=%v", e1Record, e1Err, e2Record, e2Err, e3Record, e3Err)
	}
	assertOutboxRowUnlocked(t, ctx, databaseURL, e2)
	if _, err := worker.processOne(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.processOne(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	calls := publisher.snapshot()
	target := targetCalls(calls, e1)
	if len(target) != 2 || len(calls) != 4 || !strings.HasPrefix(calls[1], e3.String()+"|") || !strings.HasPrefix(calls[3], e2.String()+"|") {
		t.Fatalf("ClaimOneRecord retry order = %v", calls)
	}
}

func TestProductionWorkerClaimOneRecoversLeaseAndRejectsStalePublisher(t *testing.T) {
	requireProductionOutboxIntegration(t)
	databaseURL := newIsolatedOutboxTestDatabaseURL(t)
	ctx := context.Background()
	dbA, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	aggregate := uuid.New()
	e1 := insertOutboxTestEventForAggregate(t, dbA, aggregate, time.Now().UTC())
	e2 := insertOutboxTestEventForAggregate(t, dbA, aggregate, time.Now().UTC())
	setOutboxNextAttempt(t, ctx, databaseURL, e2, time.Now().UTC().Add(time.Hour))
	setOutboxNextAttempt(t, ctx, databaseURL, e1, time.Unix(0, 0).UTC())
	firstPublisher := &holdingPublisher{target: e1.String(), started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(firstPublisher.releaseWait)
	secondPublisher := &recordingPublisher{}
	workerA := NewWorker(dbA, firstPublisher, Config{ClaimLease: time.Minute})
	workerB := NewWorker(dbB, secondPublisher, Config{ClaimLease: time.Minute})
	aDone := make(chan error, 1)
	go func() {
		_, processErr := workerA.processOne(ctx, time.Now().UTC())
		aDone <- processErr
	}()
	select {
	case <-firstPublisher.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker A did not claim E1")
	}
	maintenance, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.Exec(ctx, `UPDATE outbox SET claimed_at=now()-interval '1 hour' WHERE event_id=$1`, e1); err != nil {
		maintenance.Close()
		t.Fatal(err)
	}
	if _, err := workerB.processOne(ctx, time.Now().UTC()); err != nil {
		maintenance.Close()
		t.Fatal(err)
	}
	if record, err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e1); err != nil || record.Status != "PUBLISHED" || record.ClaimToken != uuid.Nil {
		maintenance.Close()
		t.Fatalf("recovered E1 = %+v, error=%v", record, err)
	}
	firstPublisher.releaseWait()
	if err := <-aDone; !errors.Is(err, postgres.ErrOutboxClaimLost) {
		maintenance.Close()
		t.Fatalf("stale publisher result = %v", err)
	}
	setOutboxNextAttempt(t, ctx, databaseURL, e2, time.Now().UTC().Add(-time.Minute))
	if _, err := workerB.processOne(ctx, time.Now().UTC()); err != nil {
		maintenance.Close()
		t.Fatal(err)
	}
	maintenance.Close()
	if record, err := postgres.NewOutboxRepository(dbB).FindByID(ctx, e2); err != nil || record.Status != "PUBLISHED" {
		t.Fatalf("E2 after recovered E1 = %+v, error=%v", record, err)
	}
}

func isolateOutboxRows(t *testing.T, ctx context.Context, databaseURL string, keep ...uuid.UUID) (*pgxpool.Pool, pgx.Tx) {
	t.Helper()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	conditions := make([]string, 0, len(keep))
	args := make([]any, 0, len(keep))
	for index, eventID := range keep {
		conditions = append(conditions, fmt.Sprintf("event_id <> $%d", index+1))
		args = append(args, eventID)
	}
	query := `UPDATE outbox SET next_attempt_at=next_attempt_at WHERE ` + strings.Join(conditions, " AND ")
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		tx.Rollback(ctx)
		pool.Close()
		t.Fatal(err)
	}
	return pool, tx
}

func assertOutboxRowUnlocked(t *testing.T, ctx context.Context, databaseURL string, eventID uuid.UUID) {
	t.Helper()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM outbox WHERE event_id=$1 FOR UPDATE NOWAIT`, eventID).Scan(&status); err != nil {
		t.Fatalf("outbox row %s is locked by fixture: %v", eventID, err)
	}
}

func requireProductionOutboxIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_OUTBOX_PRODUCTION_INTEGRATION") != "1" {
		t.Skip("RUN_OUTBOX_PRODUCTION_INTEGRATION=1 is required")
	}
}

func setOutboxNextAttempt(t *testing.T, ctx context.Context, databaseURL string, eventID uuid.UUID, nextAttemptAt time.Time) {
	t.Helper()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `UPDATE outbox SET next_attempt_at=$2 WHERE event_id=$1`, eventID, nextAttemptAt); err != nil {
		t.Fatal(err)
	}
}

func newOutboxTestDB(t *testing.T) *postgres.Repository {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	db, err := postgres.NewRepository(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// newIsolatedOutboxTestDatabaseURL creates a schema-local copy of the outbox
// table. ClaimOneRecord intentionally has global production eligibility, so
// production-path ordering tests need a separate schema rather than row locks
// that can miss rows inserted later by other integration-test packages.
func newIsolatedOutboxTestDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "outbox_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE TABLE "+schema+".outbox (LIKE public.outbox INCLUDING ALL)"); err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SEQUENCE "+schema+".outbox_ordering_seq"); err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "ALTER TABLE "+schema+".outbox ALTER COLUMN ordering_id SET DEFAULT nextval('"+schema+".outbox_ordering_seq'::regclass)"); err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return parsed.String()
}

func insertOutboxTestEvent(t *testing.T, db *postgres.Repository, now time.Time) uuid.UUID {
	return insertOutboxTestEventForAggregate(t, db, uuid.New(), now)
}

func insertOutboxTestEventForAggregate(t *testing.T, db *postgres.Repository, aggregateID uuid.UUID, now time.Time) uuid.UUID {
	t.Helper()
	eventID := uuid.New()
	if err := postgres.NewOutboxRepository(db).InsertWithMetadata(context.Background(), eventID, "TestEvent", aggregateID, "correlation", "causation", 1, now, []byte(`{"ok":true}`), time.Unix(0, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	return eventID
}

func contains(value, fragment string) bool {
	return strings.Contains(value, fragment)
}

func targetCalls(calls []string, eventID uuid.UUID) []string {
	result := make([]string, 0, len(calls))
	for _, call := range calls {
		if strings.HasPrefix(call, eventID.String()+"|") {
			result = append(result, call)
		}
	}
	return result
}

func processUntilPublished(ctx context.Context, worker *Worker, db *postgres.Repository, eventID uuid.UUID) error {
	repository := postgres.NewOutboxRepository(db)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		record, err := repository.FindByID(ctx, eventID)
		if err != nil {
			return err
		}
		if record.Status == "PUBLISHED" {
			return nil
		}
		claimed, claimErr := repository.ClaimRecordByID(ctx, eventID, time.Now().UTC(), worker.config.ClaimLease)
		if errors.Is(claimErr, pgx.ErrNoRows) {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if claimErr != nil {
			return claimErr
		}
		body, marshalErr := marshalEnvelope(claimed)
		if marshalErr != nil {
			return worker.retry(ctx, repository, claimed, time.Now().UTC(), marshalErr)
		}
		if sendErr := worker.publisher.SendEvent(ctx, body, claimed.AggregateID.String(), claimed.EventID.String()); sendErr != nil {
			if retryErr := worker.retry(ctx, repository, claimed, time.Now().UTC(), sendErr); retryErr != nil {
				return retryErr
			}
			continue
		}
		if markErr := repository.MarkPublished(ctx, claimed.EventID, claimed.ClaimToken, time.Now().UTC()); markErr != nil {
			return markErr
		}
	}
	return errors.New("outbox event was not published before deadline")
}
