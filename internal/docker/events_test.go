package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeEventsDaemon answers /events with the given JSON lines and then ends
// the stream, as a daemon that is stopping does. It keeps the filters each
// subscription asked for.
func fakeEventsDaemon(t *testing.T, stream string, filters chan<- string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
			filters <- r.URL.Query().Get("filters")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, stream)
		default:
			http.NotFound(w, r)
		}
	}))
}

// The agent sends its container list when a container is created, removed,
// renamed, started or stops. The daemon filters the stream, so a busy host's
// other events (execs, health checks, image pulls) never reach the agent.
func TestContainerEventsFiltersAndDecodes(t *testing.T) {
	filters := make(chan string, 1)
	srv := fakeEventsDaemon(t,
		`{"Type":"container","Action":"destroy","Actor":{"ID":"id-web","Attributes":{"name":"web","image":"nginx:1"}},"time":1}`+"\n"+
			`{"Type":"container","Action":"rename","Actor":{"ID":"id-db","Attributes":{"name":"database","oldName":"/db"}},"time":2}`+"\n",
		filters)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	evts, errs := pullClient(t, srv).ContainerEvents(ctx)

	var got []ContainerEvent
	for len(got) < 2 {
		select {
		case ev := <-evts:
			got = append(got, ev)
		case err := <-errs:
			t.Fatalf("stream ended after %d events: %v", len(got), err)
		case <-ctx.Done():
			t.Fatal("no events")
		}
	}
	want := []ContainerEvent{
		{Action: "destroy", ID: "id-web", Name: "web"},
		{Action: "rename", ID: "id-db", Name: "database"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("events = %+v, want %+v", got, want)
	}

	// The daemon ended the stream: one error, then the channel closes.
	select {
	case err := <-errs:
		if err == nil {
			t.Error("the end of the stream gave a nil error")
		}
	case <-ctx.Done():
		t.Fatal("the end of the stream gave no error")
	}
	select {
	case _, open := <-errs:
		if open {
			t.Error("a second error after the end of the stream")
		}
	case <-ctx.Done():
		t.Fatal("the error channel was not closed after the end of the stream")
	}

	var asked map[string]map[string]bool
	if err := json.Unmarshal([]byte(<-filters), &asked); err != nil {
		t.Fatalf("filters: %v", err)
	}
	wantFilters := map[string][]string{
		"type":  {"container"},
		"event": {"create", "destroy", "die", "rename", "start"},
	}
	for term, values := range wantFilters {
		var gotValues []string
		for v := range asked[term] {
			gotValues = append(gotValues, v)
		}
		slices.Sort(gotValues)
		if !slices.Equal(gotValues, values) {
			t.Errorf("filter %s = %v, want %v", term, gotValues, values)
		}
	}
	if len(asked) != len(wantFilters) {
		t.Errorf("filters = %v, want only %v", asked, wantFilters)
	}
}

// Ending the subscription's context ends the stream with the context's
// error, and nothing is left running.
func TestContainerEventsEndsWithItsContext(t *testing.T) {
	filters := make(chan string, 1)
	// A stream that stays open: the daemon has no events to send.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/events"):
			filters <- r.URL.Query().Get("filters")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	_, errs := pullClient(t, srv).ContainerEvents(ctx)
	<-filters
	cancel()
	select {
	case err := <-errs:
		if err == nil {
			t.Error("a cancelled subscription ended with a nil error")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the stream did not end when its context was cancelled")
	}
}
