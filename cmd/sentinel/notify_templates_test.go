package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

func TestSavedTemplateReachesProviders(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer notify.SetCustomTemplates(nil)

	a := &notifyTemplateAdapter{db}
	if err := a.SaveNotifyTemplate("update_available", "custom {{.ContainerName}}"); err != nil {
		t.Fatal(err)
	}
	ev := notify.Event{Type: notify.EventUpdateAvailable, ContainerName: "web"}
	if got := sendViaGotify(t, ev); got != "custom web" {
		t.Fatalf("saved template not active: %q", got)
	}
	if err := a.DeleteNotifyTemplate("update_available"); err != nil {
		t.Fatal(err)
	}
	if got := sendViaGotify(t, ev); got != "Container: web\n" {
		t.Fatalf("deleted template still active: %q", got)
	}
}

func sendViaGotify(t *testing.T, ev notify.Event) string {
	t.Helper()
	var msg struct{ Message string }
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&msg)
	}))
	defer srv.Close()
	if err := notify.NewGotify(srv.URL, "tok").Send(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	return msg.Message
}
