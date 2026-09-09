package httpapi

import (
	"crypto/subtle"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fallra1n/tvpoll/internal/metrics"
	"github.com/fallra1n/tvpoll/internal/ratelimit"
)

// inflightRequests tracks in-progress requests for observing saturation
// (docs/ai/01-stack.md's inflight_requests gauge) — applied to every
// router, not just the vote path, since saturation on the admin or
// public GET routers is just as worth seeing.
func inflightRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics.InflightRequests.Inc()
		defer metrics.InflightRequests.Dec()
		next.ServeHTTP(w, r)
	})
}

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
				// Retry-After isn't on the CORS response-header safelist,
				// so without this the frontend's rate-limit backoff can
				// only ever see a stripped header and falls back to a
				// guessed delay (found by running the frontend against
				// this backend in a browser, not by any unit test — see
				// docs/ai/what-ai-got-wrong.md).
				w.Header().Set("Access-Control-Expose-Headers", "Retry-After")
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
func adminAuth(token string, failLimiter *ratelimit.Limiter, trustProxyHeaders bool) func(http.Handler) http.Handler {
	want := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok := bearerToken(r)
			if ok && subtle.ConstantTimeCompare([]byte(got), want) == 1 {
				next.ServeHTTP(w, r)
				return
			}
			if !failLimiter.Allow(clientIP(r, trustProxyHeaders)) {
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

// setRateLimitHeaders answers level-1 degradation (docs/ai/02-load-model.md
// §6.7): 429 with a *jittered* Retry-After. The jitter is required, not
// cosmetic — without it every throttled client retries after exactly the
// same delay and the resulting synchronized retry wave becomes a second,
// higher peak than the first. Shared by GET /v1/polls/{id}, /token, and
// /votes.
//
// Cache-Control: no-store is set unconditionally — a 429 cached by a
// shared proxy/CDN in front of any of these endpoints would keep telling
// every other client behind that cache "too many requests" long after
// the one client that triggered it backs off.
func setRateLimitHeaders(w http.ResponseWriter) {
	retryAfter := 1 + rand.IntN(3) // 1..3s
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	w.Header().Set("Cache-Control", "no-store")
}

// writeRateLimited is the generic Error-envelope 429, used by
// GET /v1/polls/{id} and POST /token — api/openapi.yaml documents both
// via the shared TooManyRequests response, which is the Error schema.
func writeRateLimited(w http.ResponseWriter) {
	setRateLimitHeaders(w)
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
}

// clientIP uses proxy headers only when the deployment explicitly marks its
// ingress as trusted. The application port must not be directly reachable in
// that mode, otherwise clients could spoof the header to evade rate limits.
func clientIP(r *http.Request, trustProxyHeaders bool) string {
	if trustProxyHeaders {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			first, _, _ := strings.Cut(forwarded, ",")
			if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
				return ip.String()
			}
		}
		if realIP := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); realIP != nil {
			return realIP.String()
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// voteRateLimitKey scopes the shared /token+/votes bucket to
// (poll_id, ip) — see issueVoteTokenHandler's doc comment for why ip
// alone isn't enough.
func voteRateLimitKey(pollID uuid.UUID, r *http.Request, trustProxyHeaders bool) string {
	return pollID.String() + ":" + clientIP(r, trustProxyHeaders)
}
