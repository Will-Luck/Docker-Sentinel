package web

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

// With TLS configured, the first-run wizard must be served over HTTPS, not
// plain HTTP.
func TestWizardServesOverTLSWhenConfigured(t *testing.T) {
	cert, key, err := EnsureSelfSignedCert(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ws := NewWizardServer(WizardDeps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ws.SetTLS(cert, key)
	go func() { _ = ws.ListenAndServe(addr) }()
	defer func() { _ = ws.Shutdown(t.Context()) }()

	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // self-signed test certificate
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = client.Get("https://" + addr + "/setup")
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("wizard did not answer over HTTPS: %v", err)
	}
	_ = resp.Body.Close()

	if plain, err := (&http.Client{Timeout: time.Second}).Get("http://" + addr + "/setup"); err == nil {
		defer func() { _ = plain.Body.Close() }()
		if plain.StatusCode == http.StatusOK {
			t.Errorf("wizard answered a plain HTTP request with 200")
		}
	}
}
