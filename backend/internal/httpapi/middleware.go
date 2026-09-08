package httpapi

import (
	"crypto/subtle"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/fallra1n/tvpoll/internal/ratelimit"
)

// requestLogger logs one line per request at Info, with no per-vote
// payload logging on the hot path (docs/ai/01-stack.md: "на горячем пути
// логировать каждый голос нельзя" — 100k lines/sec would be its own
// outage). This middleware sits on every router including /votes, so it
// must stay cheap: no allocation beyond the format args, one line per
// request, not per Redis call.
func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// corsMiddleware allows only the configured origins, never "*" — vote
// token cookies are sent with credentials (docs/ai/01-stack.md, "Граница
// с фронтендом", п.1).
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o != "" {
			allowed[o] = true
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Vote-Token")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// adminAuth checks the static bearer token with a constant-time compare
// (docs/ai/01-stack.md: subtle.ConstantTimeCompare, not ==, to avoid a
// timing side-channel on the token comparison). failLimiter throttles
// only the failure path per source IP — legitimate authenticated admin
// traffic stays unlimited, matching "admin has auth but no rate limit"
// (docs/ai/01-stack.md).
func adminAuth(token string, failLimiter *ratelimit.Limiter) func(http.Handler) http.Handler {
	want := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok := bearerToken(r)
			if ok && subtle.ConstantTimeCompare([]byte(got), want) == 1 {
				next.ServeHTTP(w, r)
				return
			}
			if !failLimiter.Allow(clientIP(r)) {
				writeError(w, http.StatusTooManyRequests, "rate_limited", "too many failed authorization attempts")
				return
			}
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid admin bearer token")
		})
	}
}

func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return "", false
	}
	return h[len(prefix):], true
}

// writeRateLimited answers level-1 degradation (docs/ai/02-load-model.md
// §6.7): 429 with a *jittered* Retry-After. The jitter is required, not
// cosmetic — without it every throttled client retries after exactly the
// same delay and the resulting synchronized retry wave becomes a second,
// higher peak than the first. Shared by /token and /votes, which share
// one rate limit bucket per IP (docs/ai/03-deduplication.md §3.1).
func writeRateLimited(w http.ResponseWriter) {
	retryAfter := 1 + rand.IntN(3) // 1..3s
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
}

// clientIP takes the connection's remote address, not
// X-Forwarded-For/X-Real-IP — this process isn't yet configured with a
// trusted-proxy allowlist, and trusting a client-supplied header for
// rate-limit keys without one would let it be spoofed to evade the limit
// entirely.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
