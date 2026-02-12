package auth

import (
	"testing"
)

func TestRateLimiter(t *testing.T) {
	t.Run("Allow returns true initially", func(t *testing.T) {
		rl := NewRateLimiter()
		if !rl.Allow("192.168.1.1") {
			t.Error("expected Allow to return true for a new IP")
		}
	})

	t.Run("allows up to maxLoginAttempts", func(t *testing.T) {
		rl := NewRateLimiter()
		ip := "10.0.0.1"
		for i := 0; i < maxLoginAttempts; i++ {
			if !rl.Allow(ip) {
				t.Errorf("expected Allow to return true on attempt %d", i+1)
			}
		}
	})

	t.Run("blocks after exceeding maxLoginAttempts", func(t *testing.T) {
		rl := NewRateLimiter()
		ip := "10.0.0.2"
		// Use up the allowed attempts.
		for i := 0; i < maxLoginAttempts; i++ {
			rl.Allow(ip)
		}
		// The next call should set blocked (count goes to maxLoginAttempts+1).
		if rl.Allow(ip) {
			t.Error("expected Allow to return false after exceeding maxLoginAttempts")
		}
		// Subsequent calls should also be blocked.
		if rl.Allow(ip) {
			t.Error("expected Allow to remain false while blocked")
		}
	})

	t.Run("RecordFailure triggers lockout at threshold", func(t *testing.T) {
		rl := NewRateLimiter()
		ip := "10.0.0.3"
		for i := 0; i < accountLockout; i++ {
			rl.RecordFailure(ip)
		}
		// After accountLockout failures, the IP should be blocked.
		if rl.Allow(ip) {
			t.Error("expected Allow to return false after accountLockout RecordFailure calls")
		}
	})

	t.Run("Reset clears failures", func(t *testing.T) {
		rl := NewRateLimiter()
		ip := "10.0.0.4"
		// Record enough failures to trigger lockout.
		for i := 0; i < accountLockout; i++ {
			rl.RecordFailure(ip)
		}
		if rl.Allow(ip) {
			t.Error("expected blocked before reset")
		}
		rl.Reset(ip)
		if !rl.Allow(ip) {
			t.Error("expected Allow to return true after Reset")
		}
	})

	t.Run("different IPs are independent", func(t *testing.T) {
		rl := NewRateLimiter()
		ip1 := "10.0.0.10"
		ip2 := "10.0.0.11"

		// Lock out ip1.
		for i := 0; i < accountLockout; i++ {
			rl.RecordFailure(ip1)
		}
		if rl.Allow(ip1) {
			t.Error("ip1 should be blocked")
		}

		// ip2 should still be allowed.
		if !rl.Allow(ip2) {
			t.Error("ip2 should not be affected by ip1 being blocked")
		}
	})

	t.Run("Cleanup removes expired entries", func(t *testing.T) {
		rl := NewRateLimiter()
		ip := "10.0.0.20"
		// Create an entry.
		rl.Allow(ip)

		rl.mu.Lock()
		// Manually backdate the entry so it appears expired.
		a := rl.attempts[ip]
		a.FirstAt = a.FirstAt.Add(-(loginWindow + 1))
		rl.mu.Unlock()

		rl.Cleanup()

		rl.mu.Lock()
		_, exists := rl.attempts[ip]
		rl.mu.Unlock()
		if exists {
			t.Error("expected expired entry to be cleaned up")
		}
	})
}
