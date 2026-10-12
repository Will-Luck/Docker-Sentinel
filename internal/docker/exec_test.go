package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// fakeExecDaemon serves just enough of the Docker API for ExecContainer. The
// exec start call is hijacked and then held open without output until release
// is closed, like a hook command that never finishes. Exec inspect reports
// exitCode.
func fakeExecDaemon(t *testing.T, release <-chan struct{}, exitCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"exec1"}`)
		case strings.HasSuffix(r.URL.Path, "/exec/exec1/start"):
			_, _ = io.Copy(io.Discard, r.Body) // unread input makes close send a reset
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer conn.Close()
			_, _ = buf.WriteString("HTTP/1.1 101 UPGRADED\r\n" +
				"Content-Type: application/vnd.docker.raw-stream\r\n" +
				"Connection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_ = buf.Flush()
			<-release
		case strings.HasSuffix(r.URL.Path, "/exec/exec1/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"ID":"exec1","Running":false,"ExitCode":%d}`, exitCode)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestExecContainerStopsWaitingAtTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := fakeExecDaemon(t, release, 0)
	defer srv.Close()
	defer close(release)

	api, err := client.New(client.WithHost("tcp://" + srv.Listener.Addr().String()))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer api.Close()
	c := &Client{api: api}

	done := make(chan error, 1)
	go func() {
		_, _, err := c.ExecContainer(context.Background(), "c1", []string{"sleep", "999"}, 1)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when the exec outlives its timeout")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecContainer kept waiting after its 1s timeout")
	}
}

func TestExecContainerReturnsExitCode(t *testing.T) {
	release := make(chan struct{})
	close(release) // the command finishes at once
	srv := fakeExecDaemon(t, release, 3)
	defer srv.Close()

	api, err := client.New(client.WithHost("tcp://" + srv.Listener.Addr().String()))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer api.Close()
	c := &Client{api: api}

	code, _, err := c.ExecContainer(context.Background(), "c1", []string{"false"}, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
}
