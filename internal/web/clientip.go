package web

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
)

var trustedProxies atomic.Pointer[[]*net.IPNet]

// SetTrustedProxies sets the proxy networks whose X-Forwarded-For is believed.
// With none set (the default) the peer address is always the client.
func SetTrustedProxies(nets []*net.IPNet) {
	if len(nets) == 0 {
		trustedProxies.Store(nil)
		return
	}
	trustedProxies.Store(&nets)
}

// ParseTrustedProxies reads a comma-separated list of IPs and CIDR ranges
// (SENTINEL_TRUSTED_PROXIES). Entries that do not parse are skipped.
func ParseTrustedProxies(list string) []*net.IPNet {
	var out []*net.IPNet
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			ip := net.ParseIP(part)
			if ip == nil {
				continue
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			part = ip.String() + "/" + strconv.Itoa(bits)
		}
		if _, n, err := net.ParseCIDR(part); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func isTrustedProxy(ip net.IP) bool {
	nets := trustedProxies.Load()
	if nets == nil || ip == nil {
		return false
	}
	for _, n := range *nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP returns the client's address. It is the peer address, unless the
// peer is a trusted proxy: then it is the rightmost X-Forwarded-For entry that
// is not itself a trusted proxy, which a client cannot forge.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	if !isTrustedProxy(net.ParseIP(host)) {
		return host
	}
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			break
		}
		if !isTrustedProxy(ip) {
			return ip.String()
		}
	}
	return host
}
