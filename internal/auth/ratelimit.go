package auth

import (
	"sync"
	"time"
)

const cleanupInterval = 1 * time.Hour

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
	done     chan struct{} // closed by Stop() to halt the cleanup goroutine
}

// NewRateLimiter creates a new login rate limiter with a background
// goroutine that periodically removes expired entries.
func NewRateLimiter() *RateLimiter {
	rl := &RateLimiter{
		attempts: make(map[string]*LoginAttempt),
		done:     make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

// Stop stops the background cleanup goroutine. Safe to call multiple times.
func (rl *RateLimiter) Stop() {
	select {
	case <-rl.done:
		// already stopped
	default:
		close(rl.done)
	}
}

// cleanupLoop runs Cleanup every hour until Stop is called.
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.Cleanup()
		case <-rl.done:
			return
		}
	}
}

// Allow checks if a login attempt from the given IP is allowed.
// Returns true if allowed, false if rate-limited.
// This is a pure check — it does NOT increment the counter.
// Use RecordFailure to record failed attempts.
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	a, ok := rl.attempts[ip]
	if !ok {
		return true
	}

	// If blocked, check if cooldown has expired.
	if !a.BlockedAt.IsZero() {
		if time.Now().Before(a.BlockedAt.Add(accountLockoutDur)) {
			return false
		}
		// Cooldown expired — reset.
		a.Count = 0
		a.FirstAt = time.Time{}
		a.BlockedAt = time.Time{}
		return true
	}

	// Reset window if it's expired.
	if time.Now().After(a.FirstAt.Add(loginWindow)) {
		a.Count = 0
		a.FirstAt = time.Time{}
		return true
	}

	// Check if already at/above the limit.
	return a.Count < maxLoginAttempts
}

// RecordFailure records a failed login for an IP.
// This is the sole method that increments the attempt counter.
func (rl *RateLimiter) RecordFailure(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	a, ok := rl.attempts[ip]
	if !ok {
		rl.attempts[ip] = &LoginAttempt{Count: 1, FirstAt: now}
		return
	}

	// If window expired, start a new one.
	if a.FirstAt.IsZero() || now.After(a.FirstAt.Add(loginWindow)) {
		a.Count = 1
		a.FirstAt = now
		a.BlockedAt = time.Time{}
		return
	}

	a.Count++
	if a.Count >= accountLockout {
		a.BlockedAt = now
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
