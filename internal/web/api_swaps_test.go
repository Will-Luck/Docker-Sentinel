package web

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/engine"
)

// discardingUpdater is a ContainerUpdater that also discards swap records.
type discardingUpdater struct {
	err       error
	discarded []string
}

func (d *discardingUpdater) UpdateContainer(context.Context, string, string, string) error {
	return nil
}
func (d *discardingUpdater) IsUpdating(string) bool { return false }
func (d *discardingUpdater) IsIdle() bool           { return true }
func (d *discardingUpdater) SelfUpdateQueued() bool { return false }
func (d *discardingUpdater) DiscardSwap(name string) error {
	d.discarded = append(d.discarded, name)
	return d.err
}

func discardSwap(srv *Server, name string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/swaps/"+name, nil)
	req.SetPathValue("name", name)
	w := httptest.NewRecorder()
	srv.apiDiscardSwap(w, req)
	return w
}

func TestAPIDiscardSwap(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"discarded", nil, http.StatusOK},
		{"none", engine.ErrNoSwap, http.StatusNotFound},
		{"busy", engine.ErrUpdateInProgress, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := &discardingUpdater{err: tc.err}
			srv := &Server{deps: Dependencies{Updater: up, Log: slog.Default()}}

			w := discardSwap(srv, "app")

			if w.Code != tc.want || len(up.discarded) != 1 || up.discarded[0] != "app" {
				t.Fatalf("status = %d discarded = %v, want %d for app", w.Code, up.discarded, tc.want)
			}
			if tc.want == http.StatusOK {
				var body map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["status"] != "discarded" {
					t.Errorf("body = %s", w.Body.String())
				}
			}
		})
	}
}

func TestAPIDiscardSwapRejectsBadName(t *testing.T) {
	up := &discardingUpdater{}
	srv := &Server{deps: Dependencies{Updater: up, Log: slog.Default()}}

	if w := discardSwap(srv, "../etc"); w.Code != http.StatusBadRequest || len(up.discarded) != 0 {
		t.Errorf("status = %d discarded = %v, want 400 and nothing discarded", w.Code, up.discarded)
	}
}
