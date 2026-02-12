package auth

import (
	"sync"
	"time"
)

const (
	maxLoginAttempts  = 5 // per IP within the window
	loginWindow       = 5 * time.Minute
	accountLockout    = 10 // consecutive failures before lockout
	accountLockoutDur = 30 * time.Minute
)

// LoginAttempt tracks login attempts for an IP.
type LoginAttempt struct {
	Count     int
	FirstAt   time.Time
	BlockedAt time.Time // non-zero if blocked
}

// RateLimiter tracks per-IP login attempt rates.
type RateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*LoginAttempt
}

// NewRateLimiter creates a new login rate limiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		attempts: make(map[string]*LoginAttempt),
	}
}

// Allow checks if a login attempt from the given IP is allowed.
// Returns true if allowed, false if rate-limited.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	a, ok := rl.attempts[ip]
	if !ok {
		rl.attempts[ip] = &LoginAttempt{Count: 1, FirstAt: now}
		return true
	}

	// If blocked, check if cooldown has expired.
	if !a.BlockedAt.IsZero() {
		if now.Before(a.BlockedAt.Add(accountLockoutDur)) {
			return false
		}
		// Cooldown expired — reset.
		a.Count = 1
		a.FirstAt = now
		a.BlockedAt = time.Time{}
		return true
	}

	// Reset window if it's expired.
	if now.After(a.FirstAt.Add(loginWindow)) {
		a.Count = 1
		a.FirstAt = now
		return true
	}

	a.Count++
	if a.Count > maxLoginAttempts {
		a.BlockedAt = now
		return false
	}
	return true
}

// RecordFailure records a failed login for an IP. Used for exponential backoff.
func (rl *RateLimiter) RecordFailure(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	a, ok := rl.attempts[ip]
	if !ok {
		rl.attempts[ip] = &LoginAttempt{Count: 1, FirstAt: time.Now()}
		return
	}
	a.Count++
	if a.Count >= accountLockout {
		a.BlockedAt = time.Now()
	}
}

// Reset clears rate limit state for an IP (called on successful login).
func (rl *RateLimiter) Reset(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.attempts, ip)
}

// Cleanup removes expired entries. Call periodically.
func (rl *RateLimiter) Cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for ip, a := range rl.attempts {
		if !a.BlockedAt.IsZero() {
			if now.After(a.BlockedAt.Add(accountLockoutDur)) {
				delete(rl.attempts, ip)
			}
			continue
		}
		if now.After(a.FirstAt.Add(loginWindow)) {
			delete(rl.attempts, ip)
		}
	}
}
