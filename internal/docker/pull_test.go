package docker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

// fakePullDaemon answers an image pull with HTTP 200 and the given JSON
// message stream, as the daemon does once it has started writing progress.
func fakePullDaemon(t *testing.T, stream string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/images/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, stream)
		default:
			http.NotFound(w, r)
		}
	}))
}

func pullClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	api, err := client.New(client.WithHost("tcp://" + srv.Listener.Addr().String()))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { api.Close() })
	return &Client{api: api}
}

func TestPullImageReturnsErrorFromStream(t *testing.T) {
	srv := fakePullDaemon(t,
		`{"status":"Pulling from library/nginx","id":"1.27"}`+"\n"+
			`{"status":"Downloading","id":"abc123"}`+"\n"+
			`{"errorDetail":{"message":"unexpected EOF"},"error":"unexpected EOF"}`+"\n")
	defer srv.Close()

	err := pullClient(t, srv).PullImage(context.Background(), "nginx:1.27")
	if err == nil {
		t.Fatal("expected an error when the pull stream reports one")
	}
	if !strings.Contains(err.Error(), "unexpected EOF") {
		t.Errorf("error = %v, want the daemon's message", err)
	}
}

func TestPullImageSucceedsOnCleanStream(t *testing.T) {
	srv := fakePullDaemon(t,
		`{"status":"Pulling from library/nginx","id":"1.27"}`+"\n"+
			`{"status":"Digest: sha256:0000000000000000000000000000000000000000000000000000000000000000"}`+"\n"+
			`{"status":"Status: Downloaded newer image for nginx:1.27"}`+"\n")
	defer srv.Close()

	if err := pullClient(t, srv).PullImage(context.Background(), "nginx:1.27"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
