package web

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// validateExternalURL checks that rawURL is a well-formed http(s) URL pointing
// to a non-private, non-loopback address. This prevents SSRF via user-supplied
// URLs for Portainer, registries, webhooks, etc.
func validateExternalURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("URL must not be empty")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		// allowed
	default:
		return fmt.Errorf("unsupported scheme %q (must be http or https)", u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL must contain a hostname")
	}

	// Resolve to IP and check for private/loopback ranges.
	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("cannot resolve host %q: %w", host, err)
	}

	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() {
			return fmt.Errorf("loopback addresses are not allowed")
		}
		if ip.IsPrivate() {
			return fmt.Errorf("private network addresses are not allowed")
		}
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("link-local addresses are not allowed")
		}
		if ip.IsUnspecified() {
			return fmt.Errorf("unspecified addresses (0.0.0.0) are not allowed")
		}
	}

	return nil
}
