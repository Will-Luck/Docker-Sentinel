package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeNPM struct {
	name    string
	stopped bool
}

func (f *fakeNPM) TestConnection(context.Context) error                     { return nil }
func (f *fakeNPM) Lookup(uint16) *NPMResolvedURL                            { return nil }
func (f *fakeNPM) LookupForHost(uint16, string) *NPMResolvedURL             { return nil }
func (f *fakeNPM) AllMappings() map[uint16]NPMResolvedURL                   { return nil }
func (f *fakeNPM) AllMappingsGrouped() map[string]map[uint16]NPMResolvedURL { return nil }
func (f *fakeNPM) Sync(context.Context) error                               { return nil }
func (f *fakeNPM) LastSync() time.Time                                      { return time.Time{} }
func (f *fakeNPM) LastError() error                                         { return nil }
func (f *fakeNPM) Stop()                                                    { f.stopped = true }

// Testing the NPM connection builds the provider from the saved settings
// again, so a changed URL or login takes effect and the old target is dropped.
func TestNPMTestRebuildsFromSavedSettings(t *testing.T) {
	old := &fakeNPM{name: "old"}
	built := &fakeNPM{name: "new"}
	inits := 0
	srv := &Server{deps: Dependencies{
		NPM: old, SettingsStore: newMockSettingsStore(), Log: nopLogger(),
		NPMInitFunc: func(context.Context) (NPMProvider, error) { inits++; return built, nil },
	}}

	w := httptest.NewRecorder()
	srv.apiTestNPMConnection(w, httptest.NewRequest(http.MethodPost, "/api/settings/npm-test", nil))
	if inits != 1 || srv.npmProvider() != built {
		t.Errorf("provider not rebuilt: inits=%d current=%v", inits, srv.npmProvider())
	}
	if !old.stopped {
		t.Errorf("the old provider was left running")
	}
	if !strings.Contains(w.Body.String(), `"success":true`) {
		t.Errorf("test response: %s", w.Body.String())
	}
}

func TestNPMSettingsChangesDropTheStaleProvider(t *testing.T) {
	for name, call := range map[string]func(*Server, http.ResponseWriter, *http.Request){
		"url":         (*Server).apiSetNPMURL,
		"credentials": (*Server).apiSetNPMCredentials,
	} {
		t.Run(name, func(t *testing.T) {
			old := &fakeNPM{}
			srv := &Server{deps: Dependencies{NPM: old, SettingsStore: newMockSettingsStore(), Log: nopLogger()}}
			body := `{"url":"http://192.168.1.10:81","email":"a@b.c","password":"pw"}`
			call(srv, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)))
			if srv.npmProvider() != nil || !old.stopped {
				t.Errorf("the provider for the old settings is still in use")
			}
		})
	}
}

func TestNPMDisableDropsTheProvider(t *testing.T) {
	old := &fakeNPM{}
	srv := &Server{deps: Dependencies{NPM: old, SettingsStore: newMockSettingsStore(), Log: nopLogger()}}
	srv.apiSetNPMEnabled(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"enabled":false}`)))
	if srv.npmProvider() != nil || !old.stopped {
		t.Errorf("disabling NPM left the provider in use")
	}
}
