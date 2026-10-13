package web

import (
	"net/http/httptest"
	"testing"
)

// Behind a reverse proxy every client shares the proxy's address, so one
// attacker could lock everyone out. With the proxy listed as trusted, the
// real client is taken from X-Forwarded-For.
func TestClientIP_TrustedProxyUsesForwardedFor(t *testing.T) {
	SetTrustedProxies(ParseTrustedProxies("10.0.0.0/24, 192.168.1.5"))
	defer SetTrustedProxies(nil)

	mk := func(remote, xff string) string {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return clientIP(r)
	}
	if got := mk("10.0.0.7:5000", "203.0.113.9"); got != "203.0.113.9" {
		t.Errorf("trusted proxy: %q, want the forwarded client", got)
	}
	// The rightmost entry that is not itself a trusted proxy is the client; a
	// client-supplied leftmost entry is not believed.
	if got := mk("10.0.0.7:5000", "1.2.3.4, 203.0.113.9, 10.0.0.8"); got != "203.0.113.9" {
		t.Errorf("chain: %q, want 203.0.113.9", got)
	}
	if got := mk("192.168.1.5:80", "198.51.100.2"); got != "198.51.100.2" {
		t.Errorf("single-IP proxy: %q", got)
	}
	// An untrusted peer cannot choose its own address.
	if got := mk("203.0.113.50:5000", "1.2.3.4"); got != "203.0.113.50" {
		t.Errorf("untrusted peer: %q, want its own address", got)
	}
	if got := mk("10.0.0.7:5000", ""); got != "10.0.0.7" {
		t.Errorf("no header: %q", got)
	}
}

func TestClientIP_NoTrustedProxiesIgnoresForwardedFor(t *testing.T) {
	SetTrustedProxies(nil)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.7:5000"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := clientIP(r); got != "10.0.0.7" {
		t.Errorf("clientIP = %q, want the peer address when no proxy is trusted", got)
	}
}
