package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// Registries such as lscr.io and registry.gitlab.com answer an unauthenticated
// request with 401 and a Bearer challenge. FetchToken follows the challenge.
func TestFetchToken_FollowsBearerChallenge(t *testing.T) {
	var tokenQuery, tokenUser string
	withFakeRegistry(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.Header().Set("Www-Authenticate", `Bearer realm="https://auth.example/token",service="registry.example"`)
			w.WriteHeader(http.StatusUnauthorized)
		case "/token":
			tokenQuery = r.URL.RawQuery
			tokenUser, _, _ = r.BasicAuth()
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "T0KEN"})
		default:
			http.NotFound(w, r)
		}
	}))

	tok, err := FetchToken(context.Background(), "linuxserver/sabnzbd", nil, "lscr.io")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "T0KEN" {
		t.Errorf("token = %q", tok)
	}
	if tokenQuery != "scope=repository%3Alinuxserver%2Fsabnzbd%3Apull&service=registry.example" {
		t.Errorf("token request query = %q", tokenQuery)
	}
	if tokenUser != "" {
		t.Errorf("anonymous fetch sent basic auth for %q", tokenUser)
	}

	// With a stored credential it is sent to the token endpoint.
	_, err = FetchToken(context.Background(), "linuxserver/sabnzbd", &RegistryCredential{Username: "me", Secret: "pat"}, "lscr.io")
	if err != nil {
		t.Fatal(err)
	}
	if tokenUser != "me" {
		t.Errorf("credential not sent to the token endpoint, user = %q", tokenUser)
	}
}

// A registry that wants Basic auth, or none, needs no token.
func TestFetchToken_NoTokenWithoutBearerChallenge(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"open": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) },
		"basic": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Www-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
		},
	} {
		t.Run(name, func(t *testing.T) {
			withFakeRegistry(t, handler)
			tok, err := FetchToken(context.Background(), "org/app", nil, "registry.example")
			if err != nil || tok != "" {
				t.Errorf("token = %q, err = %v; want none", tok, err)
			}
		})
	}
}

func TestFetchToken_BearerTokenEndpointFailureIsAnError(t *testing.T) {
	withFakeRegistry(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.Header().Set("Www-Authenticate", `Bearer realm="https://auth.example/token",service="s"`)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	if _, err := FetchToken(context.Background(), "org/app", nil, "registry.example"); err == nil {
		t.Error("a 401 from the token endpoint was swallowed")
	}
}

func TestParseBearerChallenge(t *testing.T) {
	realm, service, ok := parseBearerChallenge(`Bearer realm="https://r/token",service="reg.example",scope="x"`)
	if !ok || realm != "https://r/token" || service != "reg.example" {
		t.Errorf("got %q %q %v", realm, service, ok)
	}
	if _, _, ok := parseBearerChallenge(`Basic realm="x"`); ok {
		t.Error("Basic parsed as Bearer")
	}
}

// An expired or revoked stored credential must not stop version detection: the
// token request is retried anonymously, which still lists the tags of a public
// image.
func TestFetchToken_FallsBackToAnonymousWhenCredentialIsRejected(t *testing.T) {
	var sawAnonymous bool
	withFakeRegistry(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, hasAuth := r.BasicAuth(); hasAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		sawAnonymous = true
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "ANON"})
	}))

	for _, host := range []string{"docker.io", "ghcr.io"} {
		sawAnonymous = false
		tok, err := FetchToken(context.Background(), "org/app", &RegistryCredential{Username: "me", Secret: "revoked"}, host)
		if err != nil || tok != "ANON" || !sawAnonymous {
			t.Errorf("%s: token=%q err=%v anonymous=%v; want an anonymous token", host, tok, err, sawAnonymous)
		}
	}
}

func TestRejectedCredentialIsRecordedForTheSettingsPage(t *testing.T) {
	ClearRejectedCredential("docker.io")
	withFakeRegistry(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "ANON"})
	}))
	_, _ = FetchToken(context.Background(), "org/app", &RegistryCredential{Username: "me", Secret: "revoked"}, "docker.io")
	if _, ok := RejectedCredentials()["docker.io"]; !ok {
		t.Error("the rejected credential was not recorded")
	}
	ClearRejectedCredential("docker.io")
	if _, ok := RejectedCredentials()["docker.io"]; ok {
		t.Error("ClearRejectedCredential left the record")
	}
}
