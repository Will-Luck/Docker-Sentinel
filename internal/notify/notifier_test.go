package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- test helpers ---

type spyLogger struct {
	infoCalls  []logCall
	errorCalls []logCall
}

type logCall struct {
	msg  string
	args []any
}

func (s *spyLogger) Info(msg string, args ...any) {
	s.infoCalls = append(s.infoCalls, logCall{msg, args})
}
func (s *spyLogger) Error(msg string, args ...any) {
	s.errorCalls = append(s.errorCalls, logCall{msg, args})
}

type stubNotifier struct {
	name string
	err  error
	sent []Event
}

func (s *stubNotifier) Name() string { return s.name }
func (s *stubNotifier) Send(_ context.Context, event Event) error {
	s.sent = append(s.sent, event)
	return s.err
}

func testEvent(t EventType) Event {
	return Event{
		Type:          t,
		ContainerName: "nginx",
		OldImage:      "nginx:1.25",
		NewImage:      "nginx:1.26",
		Timestamp:     time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC),
	}
}

// --- Multi tests ---

func TestMultiDispatchesAll(t *testing.T) {
	a := &stubNotifier{name: "a"}
	b := &stubNotifier{name: "b"}
	log := &spyLogger{}
	m := NewMulti(log, a, b)

	event := testEvent(EventUpdateSucceeded)
	m.Notify(context.Background(), event)

	if len(a.sent) != 1 {
		t.Fatalf("notifier a: got %d events, want 1", len(a.sent))
	}
	if len(b.sent) != 1 {
		t.Fatalf("notifier b: got %d events, want 1", len(b.sent))
	}
	if a.sent[0].ContainerName != "nginx" {
		t.Errorf("notifier a: container = %q, want nginx", a.sent[0].ContainerName)
	}
}

func TestMultiLogsErrorsButContinues(t *testing.T) {
	failing := &stubNotifier{name: "broken", err: errors.New("connection refused")}
	ok := &stubNotifier{name: "ok"}
	log := &spyLogger{}
	m := NewMulti(log, failing, ok)

	m.Notify(context.Background(), testEvent(EventUpdateStarted))

	// The working notifier should still receive the event.
	if len(ok.sent) != 1 {
		t.Fatalf("ok notifier: got %d events, want 1", len(ok.sent))
	}
	// The error should be logged.
	if len(log.errorCalls) != 1 {
		t.Fatalf("got %d error logs, want 1", len(log.errorCalls))
	}
	if !strings.Contains(log.errorCalls[0].msg, "notification failed") {
		t.Errorf("error log msg = %q, want 'notification failed'", log.errorCalls[0].msg)
	}
}

// --- Gotify tests ---

func TestGotifySendsCorrectRequest(t *testing.T) {
	var received gotifyMessage
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Gotify-Key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := NewGotify(srv.URL, "tok-abc")
	event := testEvent(EventUpdateSucceeded)
	err := g.Send(context.Background(), event)

	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotToken != "tok-abc" {
		t.Errorf("token = %q, want tok-abc", gotToken)
	}
	if received.Title != "Sentinel: Update Succeeded" {
		t.Errorf("title = %q, want 'Sentinel: Update Succeeded'", received.Title)
	}
	if !strings.Contains(received.Message, "nginx") {
		t.Errorf("message does not contain container name: %q", received.Message)
	}
}

func TestGotifyPriority(t *testing.T) {
	tests := []struct {
		eventType    EventType
		wantPriority int
	}{
		{EventUpdateSucceeded, 5},
		{EventUpdateAvailable, 5},
		{EventVersionAvailable, 5},
		{EventUpdateFailed, 8},
		{EventRollbackFailed, 8},
		{EventRollbackOK, 5},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			var received gotifyMessage
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &received)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			g := NewGotify(srv.URL, "tok")
			_ = g.Send(context.Background(), testEvent(tt.eventType))

			if received.Priority != tt.wantPriority {
				t.Errorf("priority = %d, want %d", received.Priority, tt.wantPriority)
			}
		})
	}
}

func TestGotifyReturnsErrorOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	g := NewGotify(srv.URL, "tok")
	err := g.Send(context.Background(), testEvent(EventUpdateStarted))

	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

// --- Webhook tests ---

func TestWebhookSendsBodyAndHeaders(t *testing.T) {
	var received Event
	var gotAuth string
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	headers := map[string]string{"Authorization": "Bearer secret123"}
	wh := NewWebhook(srv.URL, headers)
	event := testEvent(EventUpdateSucceeded)
	err := wh.Send(context.Background(), event)

	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotAuth != "Bearer secret123" {
		t.Errorf("Authorization = %q, want 'Bearer secret123'", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if received.ContainerName != "nginx" {
		t.Errorf("container = %q, want nginx", received.ContainerName)
	}
	if received.Type != EventUpdateSucceeded {
		t.Errorf("type = %q, want update_succeeded", received.Type)
	}
}

func TestWebhookReturnsErrorOnBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	wh := NewWebhook(srv.URL, nil)
	err := wh.Send(context.Background(), testEvent(EventUpdateStarted))

	if err == nil {
		t.Fatal("expected error for 403 response")
	}
}

// --- LogNotifier tests ---

func TestLogNotifierCallsLogger(t *testing.T) {
	log := &spyLogger{}
	ln := NewLogNotifier(log)

	event := testEvent(EventUpdateAvailable)
	err := ln.Send(context.Background(), event)

	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(log.infoCalls) != 1 {
		t.Fatalf("got %d info calls, want 1", len(log.infoCalls))
	}
	if log.infoCalls[0].msg != "notification event" {
		t.Errorf("msg = %q, want 'notification event'", log.infoCalls[0].msg)
	}

	// Verify structured args contain the event type.
	args := log.infoCalls[0].args
	found := false
	for i := 0; i < len(args)-1; i += 2 {
		if args[i] == "type" && args[i+1] == "update_available" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected type=update_available in log args: %v", args)
	}
}
