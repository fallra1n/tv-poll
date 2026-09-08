// Package ratelimit implements a local, per-instance, per-key token
// bucket. docs/ai/01-stack.md rejected a hand-rolled Go rate limiter for
// the *public* vote/token endpoints specifically because that limit must
// be shared across instances — but that reasoning doesn't apply
// everywhere: admin auth-failure throttling and (per review) the public
// per-IP flood limit both target "stop one script hammering one
// instance", a goal a local limiter meets at zero Redis round-trips.
package ratelimit

import (
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/time/rate"
)

// Limiter is a per-key (typically per-IP) token bucket. Buckets live in a
// bounded LRU so memory can't grow unboundedly across millions of
// distinct IPs over a broadcast; evicting a bucket just resets it to a
// fresh, full one, which fails open toward real users — the same
// generous-by-design principle applied to every other threshold in
// docs/ai/02-load-model.md §6.7 and docs/ai/03-deduplication.md §3.2.
type Limiter struct {
	mu      sync.Mutex
	buckets *lru.Cache[string, *rate.Limiter]
	rps     rate.Limit
	burst   int
}

// New builds a limiter allowing rps requests/sec sustained with the given
// burst, per key, tracking at most maxKeys distinct keys at once.
func New(rps float64, burst int, maxKeys int) *Limiter {
	cache, err := lru.New[string, *rate.Limiter](maxKeys)
	if err != nil {
		// Only returns an error for maxKeys <= 0, which is a
		// programmer error at a call site, not a runtime condition.
		panic(err)
	}
	return &Limiter{buckets: cache, rps: rate.Limit(rps), burst: burst}
}

// Allow reports whether a request for key may proceed right now,
// consuming one token if so.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets.Get(key)
	if !ok {
		b = rate.NewLimiter(l.rps, l.burst)
		l.buckets.Add(key, b)
	}
	return b.Allow()
}
