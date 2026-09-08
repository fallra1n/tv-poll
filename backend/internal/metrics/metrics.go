// Package metrics declares the Prometheus metrics from
// docs/ai/01-stack.md's "Prometheus-метрики" section — proof the
// docs/ai/02-load-model.md numbers were measured, not invented.
// Exposed on a separate listener (cmd/api/main.go, cfg.MetricsAddr),
// not the main router — see docs/ai/05-api-contract.md review, C7:
// votes_accepted_total etc. must not be reachable on the same
// unauthenticated port that serves the vote endpoints, or a poll's live
// tally leaks before the admin has published results.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	VotesAccepted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "votes_accepted_total",
		Help: "Total number of votes accepted.",
	})

	// VotesRejected's reason label matches api/openapi.yaml's
	// VoteRejected.reason enum (duplicate, closed, rate_limited) — the
	// exact same three values, not a parallel taxonomy.
	VotesRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "votes_rejected_total",
		Help: "Total number of votes rejected, by reason.",
	}, []string{"reason"})

	// Buckets tuned around the docs/ai/02-load-model.md §7 SLO (p99 vote
	// latency < 200ms), not prometheus.DefBuckets' default range, which
	// is too coarse to see anything below 5ms or above 10s.
	VoteHandlerDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "vote_handler_duration_seconds",
		Help:    "Latency of the vote-casting handler (POST /v1/polls/{id}/votes).",
		Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .2, .5, 1, 2},
	})

	RedisCommandDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "redis_command_duration_seconds",
		Help:    "Latency of Redis commands issued by this service, by command name.",
		Buckets: prometheus.DefBuckets,
	}, []string{"command"})

	InflightRequests = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "inflight_requests",
		Help: "Number of HTTP requests currently being handled by this instance.",
	})
)
