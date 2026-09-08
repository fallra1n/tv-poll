package redisstore_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/fallra1n/tvpoll/internal/store/redisstore"
)

// newTestRedis starts a real Redis 7 container — deliberately not
// miniredis, which emulates the commands this package relies on
// (SET ... NX GET EX) incompletely, and this exact combination is the
// one piece of the vote hot path that must be verified against the real
// thing (docs/ai README, "Как использовался ИИ": "проверка, а не только
// вычитывание").
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

func TestDedupStore_TryClaim_FirstClaimAccepted(t *testing.T) {
	rdb := newTestRedis(t)
	store := redisstore.NewDedupStore(rdb)
	ctx := context.Background()

	prevOptionID, accepted, err := store.TryClaim(ctx, uuid.New(), "nonce-1", 3, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !accepted {
		t.Fatal("expected the first claim to be accepted")
	}
	if prevOptionID != 0 {
		t.Errorf("prevOptionID on accept = %d, want 0", prevOptionID)
	}
}

func TestDedupStore_TryClaim_DuplicateReturnsPreviousOption(t *testing.T) {
	rdb := newTestRedis(t)
	store := redisstore.NewDedupStore(rdb)
	ctx := context.Background()
	pollID := uuid.New()

	_, accepted, err := store.TryClaim(ctx, pollID, "nonce-2", 5, time.Minute)
	if err != nil || !accepted {
		t.Fatalf("first claim: accepted=%v err=%v", accepted, err)
	}

	prevOptionID, accepted, err := store.TryClaim(ctx, pollID, "nonce-2", 9, time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if accepted {
		t.Fatal("expected the second claim with the same nonce to be rejected as a duplicate")
	}
	if prevOptionID != 5 {
		t.Errorf("prevOptionID = %d, want 5 (the option the first, successful claim recorded)", prevOptionID)
	}
}

func TestDedupStore_TryClaim_DifferentNoncesAreIndependent(t *testing.T) {
	rdb := newTestRedis(t)
	store := redisstore.NewDedupStore(rdb)
	ctx := context.Background()
	pollID := uuid.New()

	if _, accepted, err := store.TryClaim(ctx, pollID, "nonce-a", 1, time.Minute); err != nil || !accepted {
		t.Fatalf("nonce-a: accepted=%v err=%v", accepted, err)
	}
	if _, accepted, err := store.TryClaim(ctx, pollID, "nonce-b", 2, time.Minute); err != nil || !accepted {
		t.Fatalf("nonce-b: accepted=%v err=%v", accepted, err)
	}
}

func TestDedupStore_TryClaim_TTLIsSet(t *testing.T) {
	rdb := newTestRedis(t)
	store := redisstore.NewDedupStore(rdb)
	ctx := context.Background()

	if _, accepted, err := store.TryClaim(ctx, uuid.New(), "nonce-ttl", 1, time.Minute); err != nil || !accepted {
		t.Fatalf("claim: accepted=%v err=%v", accepted, err)
	}

	keys, err := rdb.Keys(ctx, "d:*").Result()
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected exactly 1 dedup key, got %d: %v", len(keys), keys)
	}

	ttl, err := rdb.TTL(ctx, keys[0]).Result()
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl <= 0 || ttl > time.Minute {
		t.Errorf("ttl = %v, want (0, 1m]", ttl)
	}
}

// TestDedupStore_TryClaim_Concurrent is exactly the concurrency test
// this project's testing strategy calls for: N goroutines claim the
// same nonce at once, and exactly one must come back accepted. This is
// the property that makes the Lua-free design (a single Redis
// SET ... NX GET) safe: correctness comes from Redis's own atomicity,
// not from any coordination on the Go side.
func TestDedupStore_TryClaim_Concurrent(t *testing.T) {
	rdb := newTestRedis(t)
	store := redisstore.NewDedupStore(rdb)
	ctx := context.Background()
	pollID := uuid.New()

	const n = 200
	var acceptedCount atomic.Int64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(optionID int) {
			defer wg.Done()
			_, accepted, err := store.TryClaim(ctx, pollID, "race-nonce", optionID%5+1, time.Minute)
			if err != nil {
				t.Errorf("TryClaim: %v", err)
				return
			}
			if accepted {
				acceptedCount.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if got := acceptedCount.Load(); got != 1 {
		t.Fatalf("expected exactly 1 accepted claim among %d concurrent attempts, got %d", n, got)
	}
}
