package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	idpClientID = "sentinel-test-client"
	idpNonce    = "test-nonce-0123456789"
	idpSubject  = "subject-42"
	idpCode     = "authorisation-code"
)

// fakeIdP is an in-process OpenID provider: a discovery document, a JWKS and
// a token endpoint that issues ID tokens signed with the configured key.
type fakeIdP struct {
	srv *httptest.Server

	mu        sync.Mutex
	keys      []json.RawMessage // JWKS "keys" entries, as published
	signKey   *rsa.PrivateKey   // key the ID token is signed with
	kid       string            // kid in the ID token header
	overrides map[string]any    // claims replacing the defaults
	verifier  string            // code_verifier last seen at the token endpoint
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	idp := &fakeIdP{kid: "key-1", overrides: map[string]any{}}
	idp.srv = httptest.NewServer(http.HandlerFunc(idp.serve))
	t.Cleanup(idp.srv.Close)
	return idp
}

func (i *fakeIdP) issuer() string { return i.srv.URL }

func (i *fakeIdP) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeJSON(w, map[string]any{
			"issuer":                                i.issuer(),
			"authorization_endpoint":                i.issuer() + "/authorize",
			"token_endpoint":                        i.issuer() + "/token",
			"jwks_uri":                              i.issuer() + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	case "/keys":
		i.mu.Lock()
		keys := append([]json.RawMessage(nil), i.keys...)
		i.mu.Unlock()
		writeJSON(w, map[string]any{"keys": keys})
	case "/token":
		if err := r.ParseForm(); err != nil || r.PostForm.Get("code") != idpCode {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		i.mu.Lock()
		i.verifier = r.PostForm.Get("code_verifier")
		idToken, err := i.signLocked()
		i.mu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"access_token": "opaque-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     idToken,
		})
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// publish adds an RSA public key to the JWKS.
func (i *fakeIdP) publish(kid string, pub *rsa.PublicKey) {
	entry, _ := json.Marshal(map[string]string{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	})
	i.mu.Lock()
	i.keys = append(i.keys, entry)
	i.mu.Unlock()
}

// publishRaw adds an arbitrary JWKS entry.
func (i *fakeIdP) publishRaw(entry string) {
	i.mu.Lock()
	i.keys = append(i.keys, json.RawMessage(entry))
	i.mu.Unlock()
}

func (i *fakeIdP) set(claim string, v any) {
	i.mu.Lock()
	i.overrides[claim] = v
	i.mu.Unlock()
}

func (i *fakeIdP) signLocked() (string, error) {
	now := time.Now()
	claims := map[string]any{
		"iss":                i.issuer(),
		"sub":                idpSubject,
		"aud":                idpClientID,
		"iat":                now.Unix(),
		"exp":                now.Add(time.Hour).Unix(),
		"nonce":              idpNonce,
		"email":              "user@example.com",
		"preferred_username": "alice",
		"groups":             []string{"ops"},
	}
	for k, v := range i.overrides {
		claims[k] = v
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": i.kid})
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.signKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func mustRSAKey(t *testing.T, bits int) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("generate %d bit RSA key: %v", bits, err)
	}
	return k
}

// newIdPProvider starts a fake IdP that signs with a fresh 2048 bit key
// published as "key-1", and runs Sentinel's discovery against it.
func newIdPProvider(t *testing.T) (*fakeIdP, *OIDCProvider) {
	t.Helper()
	idp := newFakeIdP(t)
	idp.signKey = mustRSAKey(t, 2048)
	idp.publish("key-1", &idp.signKey.PublicKey)
	return idp, discoverIdP(t, idp)
}

func discoverIdP(t *testing.T, idp *fakeIdP) *OIDCProvider {
	t.Helper()
	p, err := NewOIDCProvider(context.Background(), OIDCConfig{
		Enabled:      true,
		IssuerURL:    idp.issuer(),
		ClientID:     idpClientID,
		ClientSecret: "secret",
		RedirectURL:  "https://sentinel.example.com/auth/oidc/callback",
	})
	if err != nil {
		t.Fatalf("NewOIDCProvider: %v", err)
	}
	if p == nil {
		t.Fatal("NewOIDCProvider returned nil for an enabled config")
	}
	return p
}

func TestOIDCFakeIdP_ValidTokenAccepted(t *testing.T) {
	idp, p := newIdPProvider(t)

	verifier, err := GeneratePKCEVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if u := p.AuthURL("state", idpNonce, PKCEChallengeFromVerifier(verifier)); !strings.HasPrefix(u, idp.issuer()+"/authorize?") {
		t.Fatalf("AuthURL = %q, want the discovered authorization endpoint", u)
	}

	info, err := p.Exchange(context.Background(), idpCode, verifier, idpNonce)
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if info.Issuer != idp.issuer() {
		t.Errorf("Issuer = %q, want %q", info.Issuer, idp.issuer())
	}
	if info.Subject != idpSubject {
		t.Errorf("Subject = %q, want %q", info.Subject, idpSubject)
	}
	if info.Username != "alice" || info.Email != "user@example.com" {
		t.Errorf("claims = %q / %q, want alice / user@example.com", info.Username, info.Email)
	}
	if len(info.Groups) != 1 || info.Groups[0] != "ops" {
		t.Errorf("Groups = %v, want [ops]", info.Groups)
	}
	if idp.verifier != verifier {
		t.Errorf("token endpoint saw code_verifier %q, want %q", idp.verifier, verifier)
	}
}

func TestOIDCFakeIdP_RefusedTokens(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, idp *fakeIdP)
		wantErr string
	}{
		{
			name:    "wrong audience",
			prepare: func(t *testing.T, idp *fakeIdP) { idp.set("aud", "some-other-client") },
			wantErr: "token verification",
		},
		{
			name:    "wrong issuer",
			prepare: func(t *testing.T, idp *fakeIdP) { idp.set("iss", "https://other-issuer.example.com") },
			wantErr: "token verification",
		},
		{
			name:    "expired",
			prepare: func(t *testing.T, idp *fakeIdP) { idp.set("exp", time.Now().Add(-time.Hour).Unix()) },
			wantErr: "token verification",
		},
		{
			name: "bad signature",
			prepare: func(t *testing.T, idp *fakeIdP) {
				// Signed by a key the IdP never published, under the published kid.
				idp.signKey = mustRSAKey(t, 2048)
			},
			wantErr: "token verification",
		},
		{
			name:    "wrong nonce",
			prepare: func(t *testing.T, idp *fakeIdP) { idp.set("nonce", "a-different-nonce") },
			wantErr: "nonce mismatch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idp, p := newIdPProvider(t)
			tt.prepare(t, idp)

			info, err := p.Exchange(context.Background(), idpCode, "verifier", idpNonce)
			if err == nil {
				t.Fatalf("Exchange accepted a token that must be refused: %+v", info)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			if info != nil {
				t.Errorf("a refused exchange returned user info: %+v", info)
			}
		})
	}
}

// go-oidc v3.21.0 skips a JWKS entry it cannot parse instead of failing the
// whole set; a provider that publishes one extra key type must still sign in.
func TestOIDCFakeIdP_UnsupportedKeyTypeInJWKS(t *testing.T) {
	idp := newFakeIdP(t)
	idp.signKey = mustRSAKey(t, 2048)
	// The unsupported entry comes first so a parser that stops at it fails.
	idp.publishRaw(`{"kty":"QUANTUM","use":"sig","kid":"future-key","x":"AAAA"}`)
	idp.publish("key-1", &idp.signKey.PublicKey)
	p := discoverIdP(t, idp)

	info, err := p.Exchange(context.Background(), idpCode, "verifier", idpNonce)
	if err != nil {
		t.Fatalf("Exchange with an unsupported key type in the JWKS: %v", err)
	}
	if info.Subject != idpSubject {
		t.Errorf("Subject = %q, want %q", info.Subject, idpSubject)
	}
}

func TestOIDCFakeIdP_MixedRSAKeySizes(t *testing.T) {
	small := mustRSAKey(t, 2048)
	large := mustRSAKey(t, 4096)

	for _, tt := range []struct {
		name string
		kid  string
		key  *rsa.PrivateKey
	}{
		{"signed with the 2048 bit key", "rsa-2048", small},
		{"signed with the 4096 bit key", "rsa-4096", large},
	} {
		t.Run(tt.name, func(t *testing.T) {
			idp := newFakeIdP(t)
			idp.publish("rsa-2048", &small.PublicKey)
			idp.publish("rsa-4096", &large.PublicKey)
			idp.signKey, idp.kid = tt.key, tt.kid
			p := discoverIdP(t, idp)

			info, err := p.Exchange(context.Background(), idpCode, "verifier", idpNonce)
			if err != nil {
				t.Fatalf("Exchange: %v", err)
			}
			if info.Subject != idpSubject {
				t.Errorf("Subject = %q, want %q", info.Subject, idpSubject)
			}
		})
	}
}
