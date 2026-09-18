package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
)

type EventPublisher interface {
	SendEvent(context.Context, string, string, string) error
}

type Config struct {
	PollInterval time.Duration
	BatchSize    int
	MaxAttempts  int
	Backoff      time.Duration
	ClaimLease   time.Duration
	Logger       *slog.Logger
}

type Worker struct {
	db        *postgres.Repository
	publisher EventPublisher
	config    Config

	mu     sync.Mutex
	active *run
}

type run struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewWorker(db *postgres.Repository, publisher EventPublisher, config Config) *Worker {
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.BatchSize < 1 {
		config.BatchSize = 20
	}
	if config.MaxAttempts < 1 {
		config.MaxAttempts = 10
	}
	if config.Backoff < 0 {
		config.Backoff = 0
	}
	if config.ClaimLease <= 0 {
		config.ClaimLease = 30 * time.Second
	}
	return &Worker{db: db, publisher: publisher, config: config}
}

func (w *Worker) Start(startupCtx context.Context) error {
	if err := startupCtx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	current := &run{cancel: cancel, done: make(chan struct{})}
	w.active = current
	go func() {
		defer close(current.done)
		w.loop(ctx)
	}()
	return nil
}

func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	current := w.active
	w.mu.Unlock()
	if current == nil {
		return nil
	}
	current.cancel()
	select {
	case <-current.done:
		w.mu.Lock()
		if w.active == current {
			w.active = nil
		}
		w.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) loop(ctx context.Context) {
	for {
		processed := false
		for index := 0; index < w.config.BatchSize; index++ {
			handled, err := w.processOne(ctx, time.Now().UTC())
			if errors.Is(err, pgx.ErrNoRows) {
				break
			}
			if err != nil {
				if ctx.Err() == nil && w.config.Logger != nil {
					w.config.Logger.Error("outbox processing failed", slog.String("error", err.Error()))
				}
				break
			}
			if !handled {
				break
			}
			processed = true
		}
		if processed {
			continue
		}
		timer := time.NewTimer(w.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (w *Worker) processOne(ctx context.Context, now time.Time) (bool, error) {
	repository := postgres.NewOutboxRepository(w.db)
	record, err := repository.ClaimOneRecord(ctx, now, w.config.ClaimLease)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if err != nil {
		return false, err
	}
	body, err := marshalEnvelope(record)
	if err != nil {
		return true, w.retry(ctx, repository, record, now, err)
	}
	if err := w.publisher.SendEvent(ctx, body, record.AggregateID.String(), record.EventID.String()); err != nil {
		return true, w.retry(ctx, repository, record, now, err)
	}
	return true, repository.MarkPublished(ctx, record.EventID, record.ClaimToken, now)
}

func (w *Worker) retry(ctx context.Context, repository *postgres.OutboxRepository, record postgres.OutboxRecord, now time.Time, processingErr error) error {
	return repository.MarkRetry(ctx, record.EventID, record.ClaimToken, record.Attempts, nextAttempt(now, w.config.Backoff, record.Attempts), processingErr.Error(), w.config.MaxAttempts, now)
}

func nextAttempt(now time.Time, base time.Duration, attempts int) time.Time {
	if base <= 0 {
		return now
	}
	shift := attempts - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 30 {
		shift = 30
	}
	delay := base * time.Duration(uint64(1)<<shift)
	if delay < 0 {
		delay = 24 * time.Hour
	}
	return now.Add(delay)
}

type envelope struct {
	EventID     string          `json:"eventId"`
	EventType   string          `json:"eventType"`
	AggregateID string          `json:"aggregateId"`
	Correlation string          `json:"correlationId,omitempty"`
	Causation   string          `json:"causationId,omitempty"`
	OccurredAt  time.Time       `json:"occurredAt"`
	Version     int64           `json:"version"`
	Data        json.RawMessage `json:"data"`
}

func marshalEnvelope(record postgres.OutboxRecord) (string, error) {
	data := json.RawMessage(record.Data)
	if !json.Valid(data) {
		return "", errors.New("outbox payload is not valid JSON")
	}
	encoded, err := json.Marshal(envelope{
		EventID: record.EventID.String(), EventType: record.EventType, AggregateID: record.AggregateID.String(),
		Correlation: record.CorrelationID, Causation: record.CausationID, OccurredAt: record.OccurredAt.UTC(),
		Version: record.Version, Data: data,
	})
	return string(encoded), err
}
