package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEncodeRegistryAuth(t *testing.T) {
	enc := EncodeRegistryAuth("alice", "s3cret", "ghcr.io")
	raw, err := base64.URLEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("not URL-safe base64: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["username"] != "alice" || got["password"] != "s3cret" || got["serveraddress"] != "ghcr.io" {
		t.Errorf("decoded = %v", got)
	}
}

// A per-request auth (the hub's, on an agent) wins over the client's provider.
func TestRegistryAuthPrefersContextOverProvider(t *testing.T) {
	c := &Client{}
	c.SetRegistryAuth(func(ref string) string { return "from-provider:" + ref })

	if got := c.registryAuth(context.Background(), "ghcr.io/x/y:1"); got != "from-provider:ghcr.io/x/y:1" {
		t.Errorf("provider auth = %q", got)
	}
	ctx := WithRegistryAuth(context.Background(), "from-hub")
	if got := c.registryAuth(ctx, "ghcr.io/x/y:1"); got != "from-hub" {
		t.Errorf("context auth = %q", got)
	}
	if got := (&Client{}).registryAuth(context.Background(), "x"); got != "" {
		t.Errorf("no provider gave %q", got)
	}
}

// The pull and the digest lookup carry the credentials to the daemon.
func TestPullAndDistributionInspectSendRegistryAuth(t *testing.T) {
	headers := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/images/create"):
			headers["pull"] = r.Header.Get("X-Registry-Auth")
			_, _ = w.Write([]byte(`{"status":"done"}` + "\n"))
		case strings.Contains(r.URL.Path, "/distribution/"):
			headers["inspect"] = r.Header.Get("X-Registry-Auth")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Descriptor":{"digest":"sha256:` + strings.Repeat("a", 64) + `"}}`))
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := NewClient("tcp://"+strings.TrimPrefix(srv.URL, "http://"), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.SetRegistryAuth(func(ref string) string { return "auth-for-" + ref })

	if err := c.PullImage(context.Background(), "ghcr.io/x/y:1"); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if _, err := c.DistributionDigest(context.Background(), "ghcr.io/x/y:1"); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, k := range []string{"pull", "inspect"} {
		if headers[k] != "auth-for-ghcr.io/x/y:1" {
			t.Errorf("%s sent X-Registry-Auth %q", k, headers[k])
		}
	}
}
