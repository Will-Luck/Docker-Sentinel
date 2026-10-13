package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
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
