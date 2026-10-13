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

func digestEvent() Event {
	return Event{
		Type:           EventDigest,
		ContainerName:  "alpha",
		ContainerNames: []string{"alpha", "beta", "gamma"},
		Timestamp:      time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestFormatMessageListsEveryContainer(t *testing.T) {
	for name, got := range map[string]string{
		"plain":    formatMessage(digestEvent()),
		"markdown": formatMessageMarkdown(digestEvent()),
	} {
		for _, want := range []string{"alpha", "beta", "gamma"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s body missing %q: %q", name, want, got)
			}
		}
	}
}

func TestDiscordListsEveryContainer(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := NewDiscord(srv.URL).Send(context.Background(), digestEvent()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "beta") || !strings.Contains(string(body), "gamma") {
		t.Fatalf("discord payload does not list the containers: %s", body)
	}
}

func TestDiscordTruncatesLongError(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	ev := testEvent(EventUpdateFailed)
	ev.Error = strings.Repeat("e", 3000)
	if err := NewDiscord(srv.URL).Send(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Embeds []struct {
			Fields []struct{ Name, Value string } `json:"fields"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Embeds[0].Fields {
		if len([]rune(f.Value)) > 1024 {
			t.Fatalf("field %q is %d runes, Discord rejects over 1024", f.Name, len([]rune(f.Value)))
		}
	}
}

func TestAggregatedErrorIsBounded(t *testing.T) {
	var evs []Event
	for i := 0; i < 40; i++ {
		e := testEvent(EventUpdateFailed)
		e.ContainerName = "c" + strings.Repeat("x", 5)
		e.Error = strings.Repeat("boom ", 40)
		evs = append(evs, e)
	}
	for _, out := range aggregateEvents(evs) {
		if len([]rune(out.Error)) > 1024 {
			t.Fatalf("aggregated error is %d runes", len([]rune(out.Error)))
		}
	}
}

func TestPushoverMessageCapped(t *testing.T) {
	ev := testEvent(EventUpdateFailed)
	ev.Error = strings.Repeat("e", 3000)
	if got := len([]rune(pushoverMessage(ev))); got > 1024 {
		t.Fatalf("pushover message is %d runes, cap is 1024", got)
	}
}

func TestTelegramEscapesErrorText(t *testing.T) {
	ev := testEvent(EventUpdateFailed)
	ev.Error = "service stack_web update timed out *oops"
	got := formatTelegram(ev)
	// Legacy Markdown rejects an unmatched _ or *: they must be inside a code
	// span or escaped.
	if strings.Contains(got, "stack_web") && !strings.Contains(got, "`service stack_web") && !strings.Contains(got, `stack\_web`) {
		t.Fatalf("error text is not protected: %q", got)
	}
}

func TestCustomTemplateIsUsed(t *testing.T) {
	SetCustomTemplates(map[string]string{"update_available": "NEW {{.ContainerName}} -> {{.NewImage}} [{{.Severity}}]"})
	defer SetCustomTemplates(nil)

	var got gotifyMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	if err := NewGotify(srv.URL, "tok").Send(context.Background(), testEvent(EventUpdateAvailable)); err != nil {
		t.Fatal(err)
	}
	if want := "NEW nginx -> nginx:1.26 [info]"; got.Message != want {
		t.Fatalf("message = %q, want %q", got.Message, want)
	}
	// Other event types keep the built-in format.
	if m := formatMessage(testEvent(EventUpdateFailed)); !strings.Contains(m, "Container: nginx") {
		t.Fatalf("default format lost: %q", m)
	}
}

func TestCustomTemplateBadSyntaxFallsBack(t *testing.T) {
	SetCustomTemplates(map[string]string{"update_failed": "{{.Nope"})
	defer SetCustomTemplates(nil)
	if m := formatMessage(testEvent(EventUpdateFailed)); !strings.Contains(m, "Container: nginx") {
		t.Fatalf("expected default format, got %q", m)
	}
}

func TestTelegramLongErrorKeepsCodeSpanClosed(t *testing.T) {
	ev := testEvent(EventUpdateFailed)
	ev.Error = strings.Repeat("x", 6000)
	got := formatTelegram(ev)
	if n := strings.Count(got, "`"); n%2 != 0 {
		t.Fatalf("unbalanced backticks (%d) after truncation", n)
	}
	if len([]rune(got)) > 4096 {
		t.Fatalf("message is %d runes, over Telegram's 4096", len([]rune(got)))
	}
}

func TestDiscordRendersCustomTemplate(t *testing.T) {
	SetCustomTemplates(map[string]string{"update_available": "custom {{.ContainerName}} " + strings.Repeat("z", 5000)})
	defer SetCustomTemplates(nil)
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	if err := NewDiscord(srv.URL).Send(context.Background(), testEvent(EventUpdateAvailable)); err != nil {
		t.Fatal(err)
	}
	var p struct {
		Embeds []struct {
			Description string `json:"description"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	d := p.Embeds[0].Description
	if !strings.HasPrefix(d, "custom nginx") {
		t.Fatalf("description = %.40q", d)
	}
	if len([]rune(d)) > 4096 {
		t.Fatalf("description is %d runes, Discord caps at 4096", len([]rune(d)))
	}
}
