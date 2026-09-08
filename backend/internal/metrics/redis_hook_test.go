package metrics_test

import (
	"context"
	"net"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"

	"github.com/fallra1n/tvpoll/internal/metrics"
)

// observedCount reads the current sample count for one label of
// RedisCommandDuration — a pure package-level histogram, so tests read
// its live state rather than a fresh instance per test. Each test uses
// its own unique command name to avoid cross-test interference.
func observedCount(t *testing.T, command string) uint64 {
	t.Helper()
	obs := metrics.RedisCommandDuration.WithLabelValues(command)
	m, ok := obs.(interface{ Write(*dto.Metric) error })
	if !ok {
		t.Fatalf("observer for %q does not implement prometheus.Metric", command)
	}
	var pb dto.Metric
	if err := m.Write(&pb); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	return pb.GetHistogram().GetSampleCount()
}

func TestRedisHook_ProcessHook_RecordsObservation(t *testing.T) {
	hook := metrics.NewRedisHook()
	cmd := redis.NewStatusCmd(context.Background(), "test-process-hook")
	label := cmd.Name()
	before := observedCount(t, label)

	var nextCalled bool
	next := func(ctx context.Context, cmd redis.Cmder) error {
		nextCalled = true
		return nil
	}

	if err := hook.ProcessHook(next)(context.Background(), cmd); err != nil {
		t.Fatalf("wrapped ProcessHook: %v", err)
	}
	if !nextCalled {
		t.Fatal("expected next to be called")
	}
	if after := observedCount(t, label); after != before+1 {
		t.Errorf("observed count = %d, want %d", after, before+1)
	}
}

func TestRedisHook_ProcessHook_RecordsEvenOnError(t *testing.T) {
	hook := metrics.NewRedisHook()
	cmd := redis.NewStatusCmd(context.Background(), "test-process-hook-err")
	label := cmd.Name()
	before := observedCount(t, label)

	wantErr := context.DeadlineExceeded
	next := func(ctx context.Context, cmd redis.Cmder) error { return wantErr }

	err := hook.ProcessHook(next)(context.Background(), cmd)
	if err != wantErr {
		t.Fatalf("expected the underlying error to propagate, got %v", err)
	}
	if after := observedCount(t, label); after != before+1 {
		t.Errorf("observed count = %d, want %d (latency should be recorded even on failure)", after, before+1)
	}
}

func TestRedisHook_ProcessPipelineHook_RecordsObservation(t *testing.T) {
	hook := metrics.NewRedisHook()
	before := observedCount(t, "pipeline")

	var nextCalled bool
	next := func(ctx context.Context, cmds []redis.Cmder) error {
		nextCalled = true
		return nil
	}

	cmds := []redis.Cmder{redis.NewStatusCmd(context.Background(), "set", "k", "v")}
	if err := hook.ProcessPipelineHook(next)(context.Background(), cmds); err != nil {
		t.Fatalf("wrapped ProcessPipelineHook: %v", err)
	}
	if !nextCalled {
		t.Fatal("expected next to be called")
	}
	if after := observedCount(t, "pipeline"); after != before+1 {
		t.Errorf("observed count = %d, want %d", after, before+1)
	}
}

func TestRedisHook_DialHook_PassesThrough(t *testing.T) {
	hook := metrics.NewRedisHook()
	var called bool
	next := func(ctx context.Context, network, addr string) (net.Conn, error) {
		called = true
		return nil, nil
	}

	if _, err := hook.DialHook(next)(context.Background(), "tcp", "localhost:6379"); err != nil {
		t.Fatalf("wrapped DialHook: %v", err)
	}
	if !called {
		t.Fatal("expected next to be called")
	}
}
