package portainer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, NewClient(srv.URL, "test-token")
}

func TestClient_ListEndpoints(t *testing.T) {
	endpoints := []Endpoint{
		{ID: 1, Name: "prod", Type: EndpointDocker, Status: StatusUp},
		{ID: 2, Name: "staging", Type: EndpointAgentDocker, Status: StatusUp},
	}
	srv, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/endpoints" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("X-API-Key") != "test-token" {
			t.Errorf("missing or wrong X-API-Key header")
		}
		json.NewEncoder(w).Encode(endpoints)
	})
	_ = srv

	got, err := client.ListEndpoints(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(got))
	}
	if got[0].Name != "prod" {
		t.Errorf("expected first endpoint name 'prod', got %q", got[0].Name)
	}
	if got[1].Name != "staging" {
		t.Errorf("expected second endpoint name 'staging', got %q", got[1].Name)
	}
}

func TestClient_ListContainers(t *testing.T) {
	containers := []Container{
		{ID: "abc123", Names: []string{"/my-container"}, Image: "nginx:latest", State: "running"},
	}
	srv, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/endpoints/1/docker/containers/json" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("all") != "1" {
			t.Errorf("expected all=1 query param")
		}
		json.NewEncoder(w).Encode(containers)
	})
	_ = srv

	got, err := client.ListContainers(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 container, got %d", len(got))
	}
	if got[0].Name() != "my-container" {
		t.Errorf("expected container name 'my-container', got %q", got[0].Name())
	}
}

func TestClient_ListStacks(t *testing.T) {
	stacks := []Stack{
		{ID: 10, Name: "web-stack", Type: StackCompose, EndpointID: 1, Status: 1},
	}
	srv, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/stacks" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(stacks)
	})
	_ = srv

	got, err := client.ListStacks(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(got))
	}
	if got[0].Name != "web-stack" {
		t.Errorf("expected stack name 'web-stack', got %q", got[0].Name)
	}
}

func TestClient_TestConnection(t *testing.T) {
	srv, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]Endpoint{})
	})
	_ = srv

	if err := client.TestConnection(context.Background()); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestClient_AuthFailure(t *testing.T) {
	srv, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("Forbidden"))
	})
	_ = srv

	_, err := client.ListEndpoints(context.Background())
	if err == nil {
		t.Fatal("expected error for 403 response, got nil")
	}
}

func TestClient_RedeployStack(t *testing.T) {
	var gotBody StackRedeploy
	srv, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/api/stacks/10" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("endpointId") != "5" {
			t.Errorf("expected endpointId=5, got %q", r.URL.Query().Get("endpointId"))
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	})
	_ = srv

	env := []EnvVar{{Name: "FOO", Value: "bar"}}
	if err := client.RedeployStack(context.Background(), 10, 5, env); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gotBody.PullImage {
		t.Error("expected pullImage=true in request body")
	}
}

func TestClient_StopContainer(t *testing.T) {
	srv, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/endpoints/1/docker/containers/abc123/stop" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	_ = srv

	if err := client.StopContainer(context.Background(), 1, "abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_CreateContainer(t *testing.T) {
	srv, client := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/endpoints/1/docker/containers/create" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("name") != "my-container" {
			t.Errorf("expected name=my-container, got %q", r.URL.Query().Get("name"))
		}
		json.NewEncoder(w).Encode(ContainerCreateResponse{ID: "newid123"})
	})
	_ = srv

	id, err := client.CreateContainer(context.Background(), 1, "my-container", map[string]string{"Image": "nginx"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "newid123" {
		t.Errorf("expected ID 'newid123', got %q", id)
	}
}
