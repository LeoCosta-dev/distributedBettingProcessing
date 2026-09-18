package financial

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type ReferenceWorkerConfig struct {
	PollInterval time.Duration
	MaxAttempts  int
	Backoff      time.Duration
	Logger       *slog.Logger
}

// ReferenceWorker owns a durable retry loop. Its context is independent from
// Fx's startup context and every claim is reconstructed from PostgreSQL, so a
// restart loses no pending work and multiple instances can cooperate safely.
type ReferenceWorker struct {
	service *Service
	config  ReferenceWorkerConfig

	mu     sync.Mutex
	active *referenceWorkerRun
}

type referenceWorkerRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewReferenceWorker(service *Service, config ReferenceWorkerConfig) *ReferenceWorker {
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.MaxAttempts < 1 {
		config.MaxAttempts = 10
	}
	if config.Backoff < 0 {
		config.Backoff = 0
	}
	return &ReferenceWorker{service: service, config: config}
}

func (w *ReferenceWorker) Start(startupCtx context.Context) error {
	if err := startupCtx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active != nil {
		return nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	run := &referenceWorkerRun{cancel: cancel, done: make(chan struct{})}
	w.active = run
	go func() {
		defer close(run.done)
		w.run(runCtx)
	}()
	return nil
}

func (w *ReferenceWorker) Stop(ctx context.Context) error {
	w.mu.Lock()
	run := w.active
	w.mu.Unlock()
	if run == nil {
		return nil
	}
	run.cancel()
	select {
	case <-run.done:
		w.mu.Lock()
		if w.active == run {
			w.active = nil
		}
		w.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *ReferenceWorker) run(ctx context.Context) {
	for {
		processed, err := w.service.ProcessPendingReference(ctx, time.Now().UTC(), w.config.MaxAttempts, w.config.Backoff)
		if err != nil && ctx.Err() == nil && w.config.Logger != nil {
			w.config.Logger.Error("pending reference processing failed", slog.String("error", err.Error()))
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
