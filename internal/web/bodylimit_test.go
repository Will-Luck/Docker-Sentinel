package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The public auth endpoints decode JSON from a body nobody has vouched for,
// so it is capped.
func TestRateLimitedEndpointsCapTheBody(t *testing.T) {
	var readErr error
	h := rateLimit(newRateLimiter(10, time.Minute), func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})
	r := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(make([]byte, 1<<20)))
	h(httptest.NewRecorder(), r)
	if readErr == nil {
		t.Error("a 1 MB body was read in full on a public auth endpoint")
	}
	small := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader([]byte(`{"username":"a"}`)))
	readErr = nil
	h(httptest.NewRecorder(), small)
	if readErr != nil {
		t.Errorf("a small body was refused: %v", readErr)
	}
}

func TestLimitBodyMiddlewareCapsEveryRoute(t *testing.T) {
	var readErr error
	h := limitBody(maxRequestBody, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(make([]byte, maxRequestBody+1))))
	if readErr == nil {
		t.Error("a body over the global cap was read in full")
	}
}

// The inbound webhook only accepts the secret in a header, so the Settings
// page must not tell people to put it in the URL.
func TestSettingsPage_WebhookHelpMatchesTheHandler(t *testing.T) {
	b, _ := staticFS.ReadFile("static/settings.html")
	if strings.Contains(string(b), "?secret=") {
		t.Error("settings.html still tells people to use ?secret=, which the handler rejects")
	}
}

// The CSP only allows scripts from this origin, so the 2FA QR code library
// has to be served by Sentinel.
func TestAccountPage_QRLibraryIsSelfHosted(t *testing.T) {
	b, _ := staticFS.ReadFile("static/account.html")
	if strings.Contains(string(b), "cdn.jsdelivr.net") {
		t.Error("account.html loads the QR library from a CDN, which the CSP blocks")
	}
	srv := &Server{}
	w := httptest.NewRecorder()
	srv.serveStaticFile(w, httptest.NewRequest(http.MethodGet, "/static/qrcode.js", nil))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || w.Body.Len() < 10000 {
		t.Errorf("/static/qrcode.js: status %d, type %q, %d bytes", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
	}
}
