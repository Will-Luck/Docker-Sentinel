package web

import (
	"net"
	"net/http"
)

// clientIP extracts the IP address from r.RemoteAddr, stripping the port.
// Falls back to the raw RemoteAddr if parsing fails.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
