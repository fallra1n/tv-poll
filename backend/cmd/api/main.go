// Command api runs the public + admin + internal HTTP router on
// HTTP_ADDR, and Prometheus metrics on the separate METRICS_ADDR
// listener (docs/ai/05-api-contract.md review, C7: votes_accepted_total
// etc. must not share a port with the unauthenticated vote endpoints,
// or a poll's live tally leaks before the admin publishes it).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/fallra1n/tvpoll/internal/autoopen"
	"github.com/fallra1n/tvpoll/internal/cache"
	"github.com/fallra1n/tvpoll/internal/config"
	"github.com/fallra1n/tvpoll/internal/counter"
	"github.com/fallra1n/tvpoll/internal/httpapi"
	"github.com/fallra1n/tvpoll/internal/leader"
	"github.com/fallra1n/tvpoll/internal/metrics"
	"github.com/fallra1n/tvpoll/internal/snapshotter"
	"github.com/fallra1n/tvpoll/internal/store/postgres"
	"github.com/fallra1n/tvpoll/internal/store/redisstore"
)

// leaderLease is shared by every "one instance should do this" periodic
// job (snapshotting, auto-open) — one coordinator role per process
// fleet, not one election per job. See internal/leader's doc comment.
const leaderLease = 3 * time.Second

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddr,
		DB:   cfg.RedisDB,
	})
	defer rdb.Close()
	rdb.AddHook(metrics.NewRedisHook())

	// Fail fast on startup rather than accepting traffic against a
	// dependency that was never reachable — cheaper to debug than a
	// stream of per-request errors.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.Ping(pingCtx); err != nil {
		return err
	}
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		return err
	}

	pollStore := postgres.NewPollStore(db)
	pollCache := cache.New(pollStore, logger)
	// Refreshes scheduled/open polls every second so they're warm before
	// their voting window opens — see internal/cache's doc comment for
	// why this bounds itself instead of caching every poll forever.
	go pollCache.Run(ctx, time.Second)

	counterStore := counter.NewStore()
	voteCounters := redisstore.NewVoteCounterStore(rdb)
	snapshotStore := postgres.NewSnapshotStore(db)

	leaderElector := leader.New(rdb, "leader", leaderLease)

	snap := snapshotter.New(leaderElector, pollStore, snapshotStore, voteCounters, logger)
	go snap.Run(ctx, time.Second)

	// Review finding B4: without this, a human has to click "open" in
	// the exact second the spot airs.
	opener := autoopen.New(leaderElector, pollStore, pollCache, logger)
	go opener.Run(ctx, time.Second)

	// The counter flusher holds the only copy of not-yet-flushed vote
	// deltas, so unlike the goroutines above, the process must not exit
	// until its final post-shutdown flush has actually run — see
	// VoteCounterStore.Run's doc comment.
	var bgWG sync.WaitGroup
	bgWG.Add(1)
	go func() {
		defer bgWG.Done()
		voteCounters.Run(ctx, counterStore, time.Second, logger)
	}()

	deps := httpapi.Deps{
		Config:    cfg,
		Logger:    logger,
		DB:        db,
		Redis:     rdb,
		Polls:     pollStore,
		PollCache: pollCache,
		Dedup:     redisstore.NewDedupStore(rdb),
		Counters:  counterStore,
		Snapshots: snapshotStore,
	}

	mainSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.New(deps),
		ReadHeaderTimeout: 5 * time.Second,
	}
	metricsSrv := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           promhttp.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := mainSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		logger.Info("metrics server listening", "addr", cfg.MetricsAddr)
		if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mainSrv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		return err
	}

	// Waits for the counter flusher's final flush (see VoteCounterStore.Run)
	// so votes accepted just before shutdown still reach Redis.
	bgWG.Wait()

	logger.Info("shutdown complete")
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler)
}
