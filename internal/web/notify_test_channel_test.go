package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
)

// The per-channel Test button sends what is on screen. A channel that was
// just added, or edited but not saved, is tested as shown.
func TestApiTestNotification_UsesTheChannelSent(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer target.Close()

	settings, _ := json.Marshal(notify.WebhookSettings{URL: target.URL})
	ch := notify.Channel{ID: "new-123", Type: notify.ProviderWebhook, Name: "hook", Enabled: true, Settings: settings}
	body, _ := json.Marshal(map[string]any{"id": ch.ID, "channel": ch})

	srv := &Server{deps: Dependencies{NotifyConfig: &importNotifyStore{}, NotifyReconfigurer: &fakeReconf{}, Log: nopLogger()}}
	w := httptest.NewRecorder()
	srv.apiTestNotification(w, httptest.NewRequest(http.MethodPost, "/api/settings/notifications/test", strings.NewReader(string(body))))
	if w.Code != http.StatusOK || hits != 1 {
		t.Errorf("status %d, %d webhook calls (%s): the on-screen channel was not tested", w.Code, hits, w.Body.String())
	}
}

type fakeReconf struct{}

func (fakeReconf) Reconfigure(...notify.Notifier) {}
func (fakeReconf) SetBatchWindow(time.Duration)   {}
func (fakeReconf) SetRetry(int, time.Duration)    {}
