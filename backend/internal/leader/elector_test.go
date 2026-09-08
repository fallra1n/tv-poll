package leader_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/fallra1n/tvpoll/internal/leader"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping testcontainers test in -short mode")
	}
	ctx := context.Background()

	container, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("terminate redis container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	opts, err := redis.ParseURL(connStr)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping redis: %v", err)
	}
	return client
}

func TestElector_SoleInstanceBecomesAndStaysLeader(t *testing.T) {
	rdb := newTestRedis(t)
	e := leader.New(rdb, "test:leader", 3*time.Second)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		isLeader, err := e.IsLeader(ctx)
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if !isLeader {
			t.Fatalf("tick %d: expected the only instance to be leader", i)
		}
	}
}

// TestElector_OnlyOneOfTwoIsLeader is the direct proof of review finding
// B1: two competing electors over the same key must never both report
// leadership on the same tick.
func TestElector_OnlyOneOfTwoIsLeader(t *testing.T) {
	rdb := newTestRedis(t)
	a := leader.New(rdb, "test:leader", 3*time.Second)
	b := leader.New(rdb, "test:leader", 3*time.Second)
	ctx := context.Background()

	aLeader, err := a.IsLeader(ctx)
	if err != nil {
		t.Fatalf("a.IsLeader: %v", err)
	}
	bLeader, err := b.IsLeader(ctx)
	if err != nil {
		t.Fatalf("b.IsLeader: %v", err)
	}

	if aLeader == bLeader {
		t.Fatalf("expected exactly one of two electors to be leader, got a=%v b=%v", aLeader, bLeader)
	}
}

func TestElector_LeadershipTransfersAfterLeaseExpires(t *testing.T) {
	rdb := newTestRedis(t)
	// go-redis floors SetNX/Expire TTLs to whole seconds (confirmed
	// against the real client: a 300ms lease logs "specified duration is
	// 300ms, but minimal supported value is 1s - truncating to 1s" and
	// is silently rounded up) — 1s is the shortest lease that actually
	// behaves as requested, not an arbitrary choice. The application
	// itself never asks for a sub-second lease (leaderLease is 3s in
	// cmd/api/main.go), so this floor doesn't affect production
	// behavior, only how fast this specific test can run.
	shortLease := 1 * time.Second
	a := leader.New(rdb, "test:leader", shortLease)
	b := leader.New(rdb, "test:leader", shortLease)
	ctx := context.Background()

	aLeader, err := a.IsLeader(ctx)
	if err != nil || !aLeader {
		t.Fatalf("expected a to become leader first, got %v err=%v", aLeader, err)
	}

	// b must not take over while a's lease is still valid and a keeps
	// renewing it.
	if bLeader, err := b.IsLeader(ctx); err != nil || bLeader {
		t.Fatalf("expected b to not be leader while a's lease is active, got %v err=%v", bLeader, err)
	}

	// a stops renewing (simulating a crash); once the lease expires, b
	// must be able to take over. Sleep comfortably past shortLease to
	// absorb the second-granularity rounding above.
	time.Sleep(shortLease + 1500*time.Millisecond)

	bLeader, err := b.IsLeader(ctx)
	if err != nil {
		t.Fatalf("b.IsLeader after expiry: %v", err)
	}
	if !bLeader {
		t.Fatal("expected b to become leader once a's lease expired without renewal")
	}
}
