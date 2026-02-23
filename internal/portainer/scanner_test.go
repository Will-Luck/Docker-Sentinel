package portainer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newScannerTestServer(t *testing.T, mux *http.ServeMux) (*httptest.Server, *Scanner) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, "test-token")
	scanner := NewScanner(client)
	return srv, scanner
}

func TestScanner_Endpoints(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/endpoints", func(w http.ResponseWriter, r *http.Request) {
		endpoints := []Endpoint{
			{ID: 1, Name: "docker-up", Type: EndpointDocker, Status: StatusUp},
			{ID: 2, Name: "docker-down", Type: EndpointDocker, Status: StatusDown},
			{ID: 3, Name: "k8s-up", Type: EndpointKubernetes, Status: StatusUp},
		}
		json.NewEncoder(w).Encode(endpoints)
	})

	_, scanner := newScannerTestServer(t, mux)

	endpoints, err := scanner.Endpoints(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(endpoints))
	}
	if endpoints[0].ID != 1 {
		t.Errorf("expected endpoint ID 1, got %d", endpoints[0].ID)
	}
	if endpoints[0].Name != "docker-up" {
		t.Errorf("expected name docker-up, got %s", endpoints[0].Name)
	}
}

func TestScanner_AllEndpoints(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/endpoints", func(w http.ResponseWriter, r *http.Request) {
		endpoints := []Endpoint{
			{ID: 1, Name: "docker-up", Type: EndpointDocker, Status: StatusUp},
			{ID: 2, Name: "docker-down", Type: EndpointDocker, Status: StatusDown},
			{ID: 3, Name: "k8s-up", Type: EndpointKubernetes, Status: StatusUp},
		}
		json.NewEncoder(w).Encode(endpoints)
	})

	_, scanner := newScannerTestServer(t, mux)

	endpoints, err := scanner.AllEndpoints(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only Docker endpoints, regardless of status
	if len(endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(endpoints))
	}
}

func TestScanner_EndpointContainers(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/stacks", func(w http.ResponseWriter, r *http.Request) {
		stacks := []Stack{
			{
				ID:         10,
				Name:       "mystack",
				Type:       StackCompose,
				EndpointID: 1,
				Status:     1,
				Env:        []EnvVar{{Name: "FOO", Value: "bar"}},
			},
		}
		json.NewEncoder(w).Encode(stacks)
	})

	mux.HandleFunc("/api/endpoints/1/docker/containers/json", func(w http.ResponseWriter, r *http.Request) {
		containers := []Container{
			{
				ID:      "abc123",
				Names:   []string{"/stack-app"},
				Image:   "nginx:latest",
				ImageID: "sha256:aaa",
				State:   "running",
				Labels:  map[string]string{"com.docker.compose.project": "mystack"},
			},
			{
				ID:      "def456",
				Names:   []string{"/standalone"},
				Image:   "redis:7",
				ImageID: "sha256:bbb",
				State:   "running",
				Labels:  map[string]string{},
			},
		}
		json.NewEncoder(w).Encode(containers)
	})

	_, scanner := newScannerTestServer(t, mux)

	containers, err := scanner.EndpointContainers(context.Background(), Endpoint{ID: 1, Name: "local"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(containers))
	}

	var stackCont, standaloneCont PortainerContainer
	for _, c := range containers {
		if c.Name == "stack-app" {
			stackCont = c
		} else {
			standaloneCont = c
		}
	}

	if stackCont.StackID != 10 {
		t.Errorf("expected StackID 10, got %d", stackCont.StackID)
	}
	if stackCont.StackName != "mystack" {
		t.Errorf("expected StackName mystack, got %s", stackCont.StackName)
	}
	if stackCont.EndpointID != 1 {
		t.Errorf("expected EndpointID 1, got %d", stackCont.EndpointID)
	}
	if stackCont.EndpointName != "local" {
		t.Errorf("expected EndpointName local, got %s", stackCont.EndpointName)
	}

	if standaloneCont.StackID != 0 {
		t.Errorf("expected standalone StackID 0, got %d", standaloneCont.StackID)
	}
	if standaloneCont.StackName != "" {
		t.Errorf("expected standalone StackName empty, got %s", standaloneCont.StackName)
	}

	// Verify HostID format
	want := "portainer:1"
	if stackCont.HostID() != want {
		t.Errorf("HostID: want %s, got %s", want, stackCont.HostID())
	}
}

func TestScanner_EndpointContainers_CachesStacks(t *testing.T) {
	callCount := 0
	mux := http.NewServeMux()

	mux.HandleFunc("/api/stacks", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode([]Stack{})
	})

	mux.HandleFunc("/api/endpoints/1/docker/containers/json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]Container{})
	})
	mux.HandleFunc("/api/endpoints/2/docker/containers/json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]Container{})
	})

	_, scanner := newScannerTestServer(t, mux)

	scanner.EndpointContainers(context.Background(), Endpoint{ID: 1, Name: "ep1"})
	scanner.EndpointContainers(context.Background(), Endpoint{ID: 2, Name: "ep2"})

	if callCount != 1 {
		t.Errorf("expected stacks to be fetched once, got %d calls", callCount)
	}

	scanner.ResetCache()
	scanner.EndpointContainers(context.Background(), Endpoint{ID: 1, Name: "ep1"})

	if callCount != 2 {
		t.Errorf("expected stacks to be fetched again after reset, got %d total calls", callCount)
	}
}

func TestScanner_RedeployStack(t *testing.T) {
	var capturedBody StackRedeploy
	mux := http.NewServeMux()

	mux.HandleFunc("/api/stacks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/api/stacks", func(w http.ResponseWriter, r *http.Request) {
		stacks := []Stack{
			{
				ID:         42,
				Name:       "myapp",
				EndpointID: 1,
				Env:        []EnvVar{{Name: "DATABASE_URL", Value: "postgres://localhost/db"}},
			},
		}
		json.NewEncoder(w).Encode(stacks)
	})

	_, scanner := newScannerTestServer(t, mux)

	err := scanner.RedeployStack(context.Background(), 42, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !capturedBody.PullImage {
		t.Error("expected PullImage to be true")
	}
	if len(capturedBody.Env) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(capturedBody.Env))
	}
	if capturedBody.Env[0].Name != "DATABASE_URL" || capturedBody.Env[0].Value != "postgres://localhost/db" {
		t.Errorf("env var not preserved: got %+v", capturedBody.Env[0])
	}
}

func TestParseImageTag(t *testing.T) {
	tests := []struct {
		input     string
		wantImage string
		wantTag   string
	}{
		{"nginx:1.25", "nginx", "1.25"},
		{"ghcr.io/user/app:v2.0", "ghcr.io/user/app", "v2.0"},
		{"registry.local:5000/myapp:latest", "registry.local:5000/myapp", "latest"},
		{"nginx", "nginx", "latest"},
		{"registry.local:5000/myapp", "registry.local:5000/myapp", "latest"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotImage, gotTag := parseImageTag(tt.input)
			if gotImage != tt.wantImage {
				t.Errorf("image: want %q, got %q", tt.wantImage, gotImage)
			}
			if gotTag != tt.wantTag {
				t.Errorf("tag: want %q, got %q", tt.wantTag, gotTag)
			}
		})
	}
}
