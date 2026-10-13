package web

import (
	"net/http"
	"sync"
	"time"
)

// rateLimiter tracks request counts per IP within a sliding window.
type rateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	limit    int
	window   time.Duration
}

// newRateLimiter creates a rate limiter allowing limit requests per window per IP.
func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
}

// allow checks if the given IP is within the rate limit.
func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Prune old entries.
	entries := rl.requests[ip]
	valid := entries[:0]
	for _, t := range entries {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.limit {
		rl.requests[ip] = valid
		return false
	}

	rl.requests[ip] = append(valid, now)
	return true
}

// cleanup removes stale entries. Call periodically to prevent memory growth.
func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-rl.window)
	for ip, entries := range rl.requests {
		valid := entries[:0]
		for _, t := range entries {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(rl.requests, ip)
		} else {
			rl.requests[ip] = valid
		}
	}
}

// rateLimit wraps a handler with per-IP rate limiting.
// Returns 429 Too Many Requests when the limit is exceeded.
func rateLimit(rl *rateLimiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !rl.allow(ip) {
			writeError(w, http.StatusTooManyRequests, "too many requests, try again later")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxAuthBody)
		next(w, r)
	}
}

// maxAuthBody caps the body of the public auth endpoints (login, setup,
// passkey and TOTP steps), which take a few small JSON fields.
const maxAuthBody = 64 << 10

// maxRequestBody caps every request body. The largest legitimate one is a
// configuration import, which has its own 5 MB limit.
const maxRequestBody = 8 << 20

// limitBody caps the size of request bodies so an unauthenticated client
// cannot make the server buffer an arbitrarily large one.
func limitBody(max int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, max)
		}
		next.ServeHTTP(w, r)
	})
}
