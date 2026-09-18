package composition

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx/fxtest"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/config"
)

type lifecycleRecorder struct {
	mu       sync.Mutex
	events   *[]string
	shutdown func(context.Context) error
	deadline time.Time
}

func (r *lifecycleRecorder) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.events = append(*r.events, event)
}

func (r *lifecycleRecorder) Listen() error {
	r.record("listen")
	return nil
}

func (r *lifecycleRecorder) Addr() string { return "test-address" }

func (r *lifecycleRecorder) Serve() {
	r.record("serve")
}

func (r *lifecycleRecorder) Shutdown(ctx context.Context) error {
	r.record("shutdown")
	if deadline, ok := ctx.Deadline(); ok {
		r.mu.Lock()
		r.deadline = deadline
		r.mu.Unlock()
	}
	if r.shutdown != nil {
		return r.shutdown(ctx)
	}
	return nil
}

func (r *lifecycleRecorder) Close() error {
	r.record("close")
	return nil
}

type databaseRecorder struct{ events *[]string }

func (r databaseRecorder) Close() {
	*r.events = append(*r.events, "database-close")
}

func lifecycleLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAppendLifecycleHooksDrainsHTTPBeforeClosingDatabase(t *testing.T) {
	var events []string
	db := databaseRecorder{events: &events}
	server := &lifecycleRecorder{events: &events}
	lifecycle := fxtest.NewLifecycle(t)
	appendLifecycleHooks(lifecycle, db, server, config.Config{ShutdownTimeout: time.Second}, lifecycleLogger())

	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"listen", "serve", "shutdown", "database-close"}
	if !equalStrings(events, want) {
		t.Fatalf("lifecycle events = %v, want %v", events, want)
	}
}

func TestAppendLifecycleHooksClosesAfterConfiguredDrainTimeout(t *testing.T) {
	var events []string
	db := databaseRecorder{events: &events}
	server := &lifecycleRecorder{
		events: &events,
		shutdown: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	configured := 20 * time.Millisecond
	lifecycle := fxtest.NewLifecycle(t)
	appendLifecycleHooks(lifecycle, db, server, config.Config{ShutdownTimeout: configured}, lifecycleLogger())

	if err := lifecycle.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := lifecycle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if server.deadline.IsZero() {
		t.Fatal("shutdown did not receive a deadline")
	}
	if server.deadline.Before(started) || server.deadline.After(started.Add(250*time.Millisecond)) {
		t.Fatalf("shutdown deadline = %s, configured around %s", server.deadline, configured)
	}
	want := []string{"listen", "serve", "shutdown", "close", "database-close"}
	if !equalStrings(events, want) {
		t.Fatalf("timeout lifecycle events = %v, want %v", events, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
