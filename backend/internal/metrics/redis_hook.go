package metrics

import (
	"context"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisHook times every Redis command this service issues into
// RedisCommandDuration. Installed once on the shared client
// (cmd/api/main.go), so every call site — the dedup claim, the counter
// flush pipeline, the snapshotter's reads, leader-election commands —
// is instrumented without individual timing code at each one.
type redisHook struct{}

func NewRedisHook() redis.Hook {
	return redisHook{}
}

func (redisHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (redisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		start := time.Now()
		err := next(ctx, cmd)
		RedisCommandDuration.WithLabelValues(cmd.Name()).Observe(time.Since(start).Seconds())
		return err
	}
}

// ProcessPipelineHook covers the counter flusher's HINCRBY pipeline
// (docs/ai step 7) — timed as one "pipeline" command rather than one
// bucket per HINCRBY, since it's genuinely one round-trip.
func (redisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		start := time.Now()
		err := next(ctx, cmds)
		RedisCommandDuration.WithLabelValues("pipeline").Observe(time.Since(start).Seconds())
		return err
	}
}
