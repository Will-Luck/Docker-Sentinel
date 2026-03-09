package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sampleSendEvent returns a fully populated event for Send() tests.
func sampleSendEvent() Event {
	return Event{
		Type:          EventUpdateSucceeded,
		ContainerName: "nginx",
		OldImage:      "nginx:1.25",
		NewImage:      "nginx:1.26",
		Timestamp:     time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
}

// --- Slack Send() tests ---

func TestSlackSendSuccess(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := NewSlack(srv.URL)
	if s.Name() != "slack" {
		t.Errorf("Name() = %q, want %q", s.Name(), "slack")
	}

	err := s.Send(context.Background(), sampleSendEvent())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}

	var payload slackPayload
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal slack body: %v", err)
	}
	if !strings.Contains(payload.Text, "nginx") {
		t.Errorf("payload text missing container name: %q", payload.Text)
	}
	if !strings.Contains(payload.Text, "Update Succeeded") {
		t.Errorf("payload text missing title: %q", payload.Text)
	}
}

func TestSlackSendErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := NewSlack(srv.URL)
	err := s.Send(context.Background(), sampleSendEvent())
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "slack returned") {
		t.Errorf("error = %q, want to contain 'slack returned'", err.Error())
	}
}

// --- Discord Send() tests ---

func TestDiscordSendSuccess(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDiscord(srv.URL)
	if d.Name() != "discord" {
		t.Errorf("Name() = %q, want %q", d.Name(), "discord")
	}

	event := sampleSendEvent()
	event.Error = "test error"
	err := d.Send(context.Background(), event)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}

	var payload discordPayload
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal discord body: %v", err)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("got %d embeds, want 1", len(payload.Embeds))
	}
	embed := payload.Embeds[0]
	if !strings.Contains(embed.Title, "Update Succeeded") {
		t.Errorf("embed title = %q, want to contain 'Update Succeeded'", embed.Title)
	}
	if embed.Color != 0x2ECC71 {
		t.Errorf("embed color = 0x%X, want 0x2ECC71 (green)", embed.Color)
	}

	// Check fields include container, old image, new image, and error.
	fieldNames := make(map[string]string)
	for _, f := range embed.Fields {
		fieldNames[f.Name] = f.Value
	}
	if fieldNames["Container"] != "nginx" {
		t.Errorf("Container field = %q, want nginx", fieldNames["Container"])
	}
	if fieldNames["Old Image"] != "nginx:1.25" {
		t.Errorf("Old Image field = %q, want nginx:1.25", fieldNames["Old Image"])
	}
	if fieldNames["New Image"] != "nginx:1.26" {
		t.Errorf("New Image field = %q, want nginx:1.26", fieldNames["New Image"])
	}
	if fieldNames["Error"] != "test error" {
		t.Errorf("Error field = %q, want 'test error'", fieldNames["Error"])
	}
}

func TestDiscordSendErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	d := NewDiscord(srv.URL)
	err := d.Send(context.Background(), sampleSendEvent())
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !strings.Contains(err.Error(), "discord returned") {
		t.Errorf("error = %q, want to contain 'discord returned'", err.Error())
	}
}

func TestDiscordSendMinimalEvent(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDiscord(srv.URL)
	// Event with no optional fields: should only have Container field.
	err := d.Send(context.Background(), Event{
		Type:          EventUpdateStarted,
		ContainerName: "redis",
		Timestamp:     time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	var payload discordPayload
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Should only have Container field (no old/new image, no error).
	if len(payload.Embeds[0].Fields) != 1 {
		t.Errorf("got %d fields, want 1 (Container only)", len(payload.Embeds[0].Fields))
	}
}

// --- Ntfy Send() tests ---

func TestNtfySendWithToken(t *testing.T) {
	var gotAuth string
	var gotTitle string
	var gotPriority string
	var gotMarkdown string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTitle = r.Header.Get("X-Title")
		gotPriority = r.Header.Get("X-Priority")
		gotMarkdown = r.Header.Get("X-Markdown")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNtfy(srv.URL, "alerts", 4, "tk_test_token", "", "")
	if n.Name() != "ntfy" {
		t.Errorf("Name() = %q, want %q", n.Name(), "ntfy")
	}

	err := n.Send(context.Background(), sampleSendEvent())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotAuth != "Bearer tk_test_token" {
		t.Errorf("Authorization = %q, want 'Bearer tk_test_token'", gotAuth)
	}
	if !strings.Contains(gotTitle, "Update Succeeded") {
		t.Errorf("X-Title = %q, want to contain 'Update Succeeded'", gotTitle)
	}
	if gotPriority != "4" {
		t.Errorf("X-Priority = %q, want '4'", gotPriority)
	}
	if gotMarkdown != "true" {
		t.Errorf("X-Markdown = %q, want 'true'", gotMarkdown)
	}
	if !strings.Contains(gotBody, "nginx") {
		t.Errorf("body missing container name: %q", gotBody)
	}
}

func TestNtfySendWithBasicAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNtfy(srv.URL, "alerts", 3, "", "user", "pass")
	err := n.Send(context.Background(), sampleSendEvent())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	// Should use Basic auth, not Bearer.
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("Authorization = %q, want Basic auth", gotAuth)
	}
}

func TestNtfySendNoAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNtfy(srv.URL, "public-topic", 3, "", "", "")
	err := n.Send(context.Background(), sampleSendEvent())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (no auth)", gotAuth)
	}
}

func TestNtfySendURLConstruction(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Ensure trailing slash on server is stripped.
	n := NewNtfy(srv.URL+"/", "my-topic", 3, "", "", "")
	err := n.Send(context.Background(), sampleSendEvent())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotPath != "/my-topic" {
		t.Errorf("path = %q, want /my-topic", gotPath)
	}
}

func TestNtfySendErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	n := NewNtfy(srv.URL, "alerts", 3, "", "", "")
	err := n.Send(context.Background(), sampleSendEvent())
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "ntfy returned") {
		t.Errorf("error = %q, want to contain 'ntfy returned'", err.Error())
	}
}

// --- Telegram Send() tests ---
// Telegram hardcodes api.telegram.org, so we override the client's transport
// to intercept and redirect requests to our test server.

func TestTelegramSendSuccess(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tg := NewTelegram("test-bot-token", "12345")
	// Override client to redirect to test server.
	tg.client = srv.Client()
	tg.client.Transport = rewriteTransport{base: srv.URL}

	err := tg.Send(context.Background(), sampleSendEvent())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	// Path should contain the bot token.
	if !strings.Contains(gotPath, "bottest-bot-token") {
		t.Errorf("path = %q, want to contain 'bottest-bot-token'", gotPath)
	}

	var payload telegramPayload
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal telegram body: %v", err)
	}
	if payload.ChatID != "12345" {
		t.Errorf("chat_id = %q, want '12345'", payload.ChatID)
	}
	if payload.ParseMode != "Markdown" {
		t.Errorf("parse_mode = %q, want 'Markdown'", payload.ParseMode)
	}
	if !strings.Contains(payload.Text, "nginx") {
		t.Errorf("text missing container name: %q", payload.Text)
	}
}

func TestTelegramSendErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	tg := NewTelegram("tok", "999")
	tg.client = srv.Client()
	tg.client.Transport = rewriteTransport{base: srv.URL}

	err := tg.Send(context.Background(), sampleSendEvent())
	if err == nil {
		t.Fatal("expected error for 403 response")
	}
	if !strings.Contains(err.Error(), "telegram returned") {
		t.Errorf("error = %q, want to contain 'telegram returned'", err.Error())
	}
}

func TestTelegramName(t *testing.T) {
	tg := NewTelegram("tok", "999")
	if tg.Name() != "telegram" {
		t.Errorf("Name() = %q, want 'telegram'", tg.Name())
	}
}

// --- Pushover Send() tests ---
// Pushover also hardcodes its endpoint, so we use the same transport trick.

func TestPushoverSendSuccess(t *testing.T) {
	var gotBody string
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewPushover("app-token-123", "user-key-456")
	p.client = srv.Client()
	p.client.Transport = rewriteTransport{base: srv.URL}

	if p.Name() != "pushover" {
		t.Errorf("Name() = %q, want 'pushover'", p.Name())
	}

	err := p.Send(context.Background(), sampleSendEvent())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotContentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", gotContentType)
	}
	// Verify form fields are present.
	if !strings.Contains(gotBody, "token=app-token-123") {
		t.Errorf("body missing token: %q", gotBody)
	}
	if !strings.Contains(gotBody, "user=user-key-456") {
		t.Errorf("body missing user key: %q", gotBody)
	}
	if !strings.Contains(gotBody, "title=") {
		t.Errorf("body missing title: %q", gotBody)
	}
	if !strings.Contains(gotBody, "message=") {
		t.Errorf("body missing message: %q", gotBody)
	}
}

func TestPushoverSendErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	p := NewPushover("tok", "key")
	p.client = srv.Client()
	p.client.Transport = rewriteTransport{base: srv.URL}

	err := p.Send(context.Background(), sampleSendEvent())
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "pushover returned") {
		t.Errorf("error = %q, want to contain 'pushover returned'", err.Error())
	}
}

// --- Apprise Send() tests ---

func TestAppriseSendWithTag(t *testing.T) {
	var gotBody []byte
	var gotPath string
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewApprise(srv.URL, "alerts", "")
	if a.Name() != "apprise" {
		t.Errorf("Name() = %q, want 'apprise'", a.Name())
	}

	err := a.Send(context.Background(), sampleSendEvent())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	// When tag is set, endpoint should be /notify/<tag>.
	if gotPath != "/notify/alerts" {
		t.Errorf("path = %q, want /notify/alerts", gotPath)
	}

	var payload map[string]string
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal apprise body: %v", err)
	}
	if payload["type"] != "success" {
		t.Errorf("type = %q, want 'success' for update_succeeded", payload["type"])
	}
	if payload["title"] == "" {
		t.Error("title should not be empty")
	}
	if payload["body"] == "" {
		t.Error("body should not be empty")
	}
	// When using tag, urls should not be in payload.
	if _, ok := payload["urls"]; ok {
		t.Error("urls should not be present when using tag")
	}
}

func TestAppriseSendWithUrls(t *testing.T) {
	var gotBody []byte
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := NewApprise(srv.URL, "", "slack://xoxb-token/channel")
	err := a.Send(context.Background(), sampleSendEvent())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	// When no tag, endpoint should be /notify/ and urls should be in payload.
	if gotPath != "/notify/" {
		t.Errorf("path = %q, want /notify/", gotPath)
	}

	var payload map[string]string
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["urls"] != "slack://xoxb-token/channel" {
		t.Errorf("urls = %q, want 'slack://xoxb-token/channel'", payload["urls"])
	}
}

func TestAppriseSendMsgType(t *testing.T) {
	tests := []struct {
		eventType EventType
		wantType  string
	}{
		{EventUpdateSucceeded, "success"},
		{EventRollbackOK, "success"},
		{EventUpdateFailed, "failure"},
		{EventRollbackFailed, "failure"},
		{EventUpdateAvailable, "info"},
		{EventUpdateStarted, "info"},
		{EventDigest, "info"},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			a := NewApprise(srv.URL, "test", "")
			err := a.Send(context.Background(), Event{
				Type:          tt.eventType,
				ContainerName: "test",
				Timestamp:     time.Now(),
			})
			if err != nil {
				t.Fatalf("Send() error = %v", err)
			}

			var payload map[string]string
			if err := json.Unmarshal(gotBody, &payload); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if payload["type"] != tt.wantType {
				t.Errorf("type = %q, want %q", payload["type"], tt.wantType)
			}
		})
	}
}

func TestAppriseSendErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	a := NewApprise(srv.URL, "test", "")
	err := a.Send(context.Background(), sampleSendEvent())
	if err == nil {
		t.Fatal("expected error for 503 response")
	}
	if !strings.Contains(err.Error(), "apprise returned") {
		t.Errorf("error = %q, want to contain 'apprise returned'", err.Error())
	}
}

func TestAppriseSendTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Ensure trailing slash on URL is stripped (no double slash).
	a := NewApprise(srv.URL+"/", "tag", "")
	err := a.Send(context.Background(), sampleSendEvent())
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotPath != "/notify/tag" {
		t.Errorf("path = %q, want /notify/tag", gotPath)
	}
}

// --- rewriteTransport ---
// Redirects all requests to the test server, preserving path and method.

type rewriteTransport struct {
	base string // test server URL, e.g. "http://127.0.0.1:PORT"
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace the scheme+host with the test server, keeping the path.
	newURL := rt.base + req.URL.Path
	if req.URL.RawQuery != "" {
		newURL += "?" + req.URL.RawQuery
	}
	newReq, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return http.DefaultTransport.RoundTrip(newReq)
}
