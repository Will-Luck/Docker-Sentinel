package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// blockingProvider holds every remote call until release is closed.
type blockingProvider struct {
	mockClusterProvider
	started chan struct{}
	release chan struct{}
}

func (b *blockingProvider) UpdateRemoteContainer(context.Context, string, string, string, string) error {
	b.started <- struct{}{}
	<-b.release
	return nil
}

func (b *blockingProvider) RemoteContainerAction(context.Context, string, string, string) error {
	b.started <- struct{}{}
	<-b.release
	return nil
}

func (b *blockingProvider) RemoteContainerLogs(context.Context, string, string, int) (string, error) {
	b.started <- struct{}{}
	<-b.release
	return "", nil
}

// cluster-server-5, remote-orchestration-16: a remote update, action or log
// fetch held the controller's lock for its whole run, so turning cluster
// mode off (SetProvider) waited behind it, and every cluster-aware
// dashboard request behind that. The calls run outside the lock.
func TestClusterController_RemoteCallsDoNotHoldTheLock(t *testing.T) {
	calls := map[string]func(*ClusterController){
		"update": func(c *ClusterController) {
			_ = c.UpdateRemoteContainer(context.Background(), "h1", "web", "nginx:2", "")
		},
		"action": func(c *ClusterController) { _ = c.RemoteContainerAction(context.Background(), "h1", "web", "restart") },
		"logs":   func(c *ClusterController) { _, _ = c.RemoteContainerLogs(context.Background(), "h1", "web", 10) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			p := &blockingProvider{started: make(chan struct{}, 1), release: make(chan struct{})}
			cc := NewClusterController()
			cc.SetProvider(p)
			go call(cc)
			<-p.started
			done := make(chan struct{})
			go func() {
				cc.SetProvider(nil)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("SetProvider waited behind a running remote call")
			}
			close(p.release)
		})
	}
}

// resumableProvider records pauses and resumes.
type resumableProvider struct {
	mockClusterProvider
	calls []string
}

func (r *resumableProvider) PauseHost(id string) error {
	r.calls = append(r.calls, "pause "+id)
	return nil
}

func (r *resumableProvider) ResumeHost(id string) error {
	r.calls = append(r.calls, "resume "+id)
	return nil
}

// cluster-server-6: the cluster page's Drain button posted to a route that
// does not exist, and a paused host could never be resumed. Pause and
// resume both reach the cluster.
func TestHandlePauseAndResumeHost(t *testing.T) {
	p := &resumableProvider{}
	cc := NewClusterController()
	cc.SetProvider(p)
	srv := &Server{deps: Dependencies{Cluster: cc}}
	for _, h := range []func(http.ResponseWriter, *http.Request){srv.handlePauseHost, srv.handleResumeHost} {
		r := httptest.NewRequest(http.MethodPost, "/api/cluster/hosts/h1/x", nil)
		r.SetPathValue("id", "h1")
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status %d: %s", w.Code, w.Body.String())
		}
	}
	if fmt.Sprint(p.calls) != "[pause h1 resume h1]" {
		t.Errorf("calls = %v, want a pause then a resume of h1", p.calls)
	}
}
