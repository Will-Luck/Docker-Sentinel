package docker

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePingDaemon answers /_ping as a daemon speaking the given API version.
func fakePingDaemon(t *testing.T, apiVersion string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("Api-Version", apiVersion)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
}

func TestNewClientRejectsDaemonBelowMinimumAPI(t *testing.T) {
	srv := fakePingDaemon(t, "1.43") // Docker 24
	defer srv.Close()

	c, err := NewClient("tcp://"+srv.Listener.Addr().String(), nil)
	if err == nil {
		c.Close()
		t.Fatal("expected an error for a daemon older than API 1.44")
	}
	if !strings.Contains(err.Error(), "API 1.43") || !strings.Contains(err.Error(), "Docker Engine 25.0") {
		t.Errorf("error = %v, want it to name the daemon's API version and Docker Engine 25.0", err)
	}
}

func TestNewClientAcceptsSupportedDaemon(t *testing.T) {
	for _, v := range []string{"1.44", "1.54"} {
		srv := fakePingDaemon(t, v)
		c, err := NewClient("tcp://"+srv.Listener.Addr().String(), nil)
		if err != nil {
			t.Errorf("API %s: unexpected error: %v", v, err)
		} else {
			if got := c.api.ClientVersion(); got != v {
				t.Errorf("API %s: negotiated %s", v, got)
			}
			c.Close()
		}
		srv.Close()
	}
}
