package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

const (
	passkeyTestRPID   = "sentinel.example.com"
	passkeyTestOrigin = "https://sentinel.example.com"
	passkeyCredIDLen  = 16
)

// Authenticator data flags (WebAuthn section 6.1).
const (
	adFlagUP = 0x01 // user present
	adFlagUV = 0x04 // user verified
	adFlagBE = 0x08 // backup eligible
	adFlagBS = 0x10 // backed up
	adFlagAT = 0x40 // attested credential data included
)

var b64url = base64.RawURLEncoding

// newPasskeyTestServer builds a Server on a real database, wired as main
// wires it: the store holds credentials whether or not passkeys are on. With
// passkeys false, SetWebAuthn is never called, as when the RPID is unusable.
func newPasskeyTestServer(t *testing.T, passkeys bool) (*Server, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "sentinel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.EnsureAuthBuckets(); err != nil {
		t.Fatalf("EnsureAuthBuckets: %v", err)
	}
	if err := db.SeedBuiltinRoles(); err != nil {
		t.Fatalf("SeedBuiltinRoles: %v", err)
	}
	enabled := true
	svc := auth.NewService(auth.ServiceConfig{
		Users:          db,
		Sessions:       db,
		Roles:          db,
		Tokens:         db,
		Settings:       db,
		WebAuthnCreds:  db,
		PendingTOTP:    db,
		Log:            slog.Default(),
		SessionExpiry:  time.Hour,
		AuthEnabledEnv: &enabled,
	})
	srv := &Server{
		deps: Dependencies{
			Auth:     svc,
			EventBus: events.New(),
			EventLog: &mockEventLogger{},
			Queue:    &mockUpdateQueue{},
			Log:      slog.Default(),
		},
		authLimiter: newRateLimiter(10, time.Minute),
	}
	if passkeys {
		wa, err := webauthn.New(&webauthn.Config{
			RPDisplayName: "Docker-Sentinel",
			RPID:          passkeyTestRPID,
			RPOrigins:     []string{passkeyTestOrigin},
		})
		if err != nil {
			t.Fatalf("webauthn.New: %v", err)
		}
		srv.SetWebAuthn(wa)
	}
	return srv, db
}

// createPasskeyUser stores a user with no password: passkey tests need
// none, and skipping the hash keeps them fast.
func createPasskeyUser(t *testing.T, db *store.Store, username string) auth.User {
	t.Helper()
	id, err := auth.GenerateUserID()
	if err != nil {
		t.Fatal(err)
	}
	user := auth.User{
		ID:        id,
		Username:  username,
		RoleID:    auth.RoleAdminID,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := db.CreateUser(user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return user
}

// softAuthenticator is a software ES256 authenticator holding one passkey.
type softAuthenticator struct {
	credID []byte
	key    *ecdsa.PrivateKey
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, passkeyCredIDLen)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return &softAuthenticator{credID: id, key: key}
}

// passkeyAuthData starts authenticator data for the test RP: RP ID hash,
// flags and a zero sign count.
func passkeyAuthData(flags byte) *bytes.Buffer {
	rpHash := sha256.Sum256([]byte(passkeyTestRPID))
	ad := bytes.NewBuffer(append([]byte{}, rpHash[:]...))
	ad.WriteByte(flags)
	ad.Write([]byte{0, 0, 0, 0})
	return ad
}

func passkeyClientData(typ, challenge string) []byte {
	cd, _ := json.Marshal(map[string]any{
		"type":        typ,
		"challenge":   challenge,
		"origin":      passkeyTestOrigin,
		"crossOrigin": false,
	})
	return cd
}

// attestation answers a registration challenge the way a synced passkey does:
// "none" attestation, backup eligible and backed up.
func (a *softAuthenticator) attestation(t *testing.T, challenge string) []byte {
	t.Helper()
	em, err := cbor.CTAP2EncOptions().EncMode()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := a.key.PublicKey.Bytes() // uncompressed point: 0x04, X, Y
	if err != nil {
		t.Fatal(err)
	}
	x, y := pub[1:33], pub[33:]
	cose, err := em.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y}) // EC2, ES256, P-256
	if err != nil {
		t.Fatal(err)
	}
	ad := passkeyAuthData(adFlagUP | adFlagUV | adFlagBE | adFlagBS | adFlagAT)
	ad.Write(make([]byte, 16)) // AAGUID
	ad.Write([]byte{0, passkeyCredIDLen})
	ad.Write(a.credID)
	ad.Write(cose)
	attObj, err := em.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": ad.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"id":    b64url.EncodeToString(a.credID),
		"rawId": b64url.EncodeToString(a.credID),
		"type":  "public-key",
		"response": map[string]any{
			"attestationObject": b64url.EncodeToString(attObj),
			"clientDataJSON":    b64url.EncodeToString(passkeyClientData("webauthn.create", challenge)),
			"transports":        []string{"internal", "hybrid"},
		},
	})
	return body
}

// assertion answers a login challenge, signing with the authenticator's key.
func (a *softAuthenticator) assertion(t *testing.T, challenge string, userHandle []byte) []byte {
	t.Helper()
	cd := passkeyClientData("webauthn.get", challenge)
	ad := passkeyAuthData(adFlagUP | adFlagUV | adFlagBE | adFlagBS).Bytes()
	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"id":    b64url.EncodeToString(a.credID),
		"rawId": b64url.EncodeToString(a.credID),
		"type":  "public-key",
		"response": map[string]any{
			"authenticatorData": b64url.EncodeToString(ad),
			"clientDataJSON":    b64url.EncodeToString(cd),
			"signature":         b64url.EncodeToString(sig),
			"userHandle":        b64url.EncodeToString(userHandle),
		},
	})
	return body
}

// passkeyOptions is the part of the begin responses the browser reads.
type passkeyOptions struct {
	PublicKey struct {
		Challenge              string         `json:"challenge"`
		AuthenticatorSelection map[string]any `json:"authenticatorSelection"`
		ExcludeCredentials     []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"excludeCredentials"`
	} `json:"publicKey"`
}

func beginPasskeyRegistration(t *testing.T, srv *Server, userID string) passkeyOptions {
	t.Helper()
	user, err := srv.deps.Auth.Users.GetUser(userID)
	if err != nil || user == nil {
		t.Fatalf("GetUser(%s): %v", userID, err)
	}
	w := httptest.NewRecorder()
	r := reqWithAuthContext(httptest.NewRequest(http.MethodPost, "/api/auth/passkeys/register/begin", nil), user)
	srv.apiPasskeyRegisterBegin(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("register begin: status = %d, body: %s", w.Code, w.Body.String())
	}
	var opts passkeyOptions
	if err := json.Unmarshal(w.Body.Bytes(), &opts); err != nil {
		t.Fatalf("decode creation options: %v", err)
	}
	return opts
}

// registerPasskey registers a's passkey for the user through the handlers.
func registerPasskey(t *testing.T, srv *Server, userID string, a *softAuthenticator) {
	t.Helper()
	opts := beginPasskeyRegistration(t, srv, userID)
	// Each request loads the user afresh, so finish sees the handle begin saved.
	user, _ := srv.deps.Auth.Users.GetUser(userID)
	body := a.attestation(t, opts.PublicKey.Challenge)
	w := httptest.NewRecorder()
	r := reqWithAuthContext(httptest.NewRequest(http.MethodPost, "/api/auth/passkeys/register/finish", bytes.NewReader(body)), user)
	srv.apiPasskeyRegisterFinish(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("register finish: status = %d, body: %s", w.Code, w.Body.String())
	}
}

// loginWithPasskey runs the discoverable login through the handlers and
// returns the finish response.
func loginWithPasskey(t *testing.T, srv *Server, a *softAuthenticator, userHandle []byte) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	srv.apiPasskeyLoginBegin(w, httptest.NewRequest(http.MethodPost, "/api/auth/passkeys/login/begin", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("login begin: status = %d, body: %s", w.Code, w.Body.String())
	}
	var opts passkeyOptions
	if err := json.Unmarshal(w.Body.Bytes(), &opts); err != nil {
		t.Fatalf("decode assertion options: %v", err)
	}
	var ceremony *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "sentinel_webauthn_session" {
			ceremony = c
		}
	}
	if ceremony == nil {
		t.Fatal("login begin set no ceremony cookie")
	}

	body := a.assertion(t, opts.PublicKey.Challenge, userHandle)
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/auth/passkeys/login/finish", bytes.NewReader(body))
	r.AddCookie(ceremony)
	srv.apiPasskeyLoginFinish(w, r)
	return w
}

// assertPasskeySignIn checks that a login finish response signed the user in.
func assertPasskeySignIn(t *testing.T, srv *Server, w *httptest.ResponseRecorder, userID string) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("login finish: status = %d, body: %s", w.Code, w.Body.String())
	}
	var token string
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("login finish set no session cookie")
	}
	sess, err := srv.deps.Auth.Sessions.GetSession(token)
	if err != nil || sess == nil || sess.UserID != userID {
		t.Fatalf("session for the cookie = %+v (%v), want one for user %s", sess, err, userID)
	}
}

// The registration options must ask for a discoverable credential, because
// sign-in is discoverable only (no allow list): a hardware key told nothing
// may create a credential that sign-in can never find.
func TestPasskeyRegistrationOptions(t *testing.T) {
	srv, db := newPasskeyTestServer(t, true)
	user := createPasskeyUser(t, db, "alice")

	sel := beginPasskeyRegistration(t, srv, user.ID).PublicKey.AuthenticatorSelection

	for field, want := range map[string]any{
		"residentKey":        "preferred",
		"userVerification":   "preferred",
		"requireResidentKey": false,
	} {
		if got, ok := sel[field]; !ok || got != want {
			t.Errorf("authenticatorSelection.%s = %v (present %t), want %v; options: %v", field, got, ok, want, sel)
		}
	}
}

// A second registration lists the user's passkeys as excluded, so the browser
// refuses an authenticator that already holds one. Without that, a resident
// key registered again replaces the first on the authenticator and leaves its
// database entry unusable. Another user's passkey is not excluded.
func TestPasskeyRegistrationExcludesRegisteredPasskeys(t *testing.T) {
	srv, db := newPasskeyTestServer(t, true)
	alice := createPasskeyUser(t, db, "alice")
	bob := createPasskeyUser(t, db, "bob")
	a := newSoftAuthenticator(t)
	b := newSoftAuthenticator(t)
	registerPasskey(t, srv, alice.ID, a)
	registerPasskey(t, srv, bob.ID, b)

	excluded := beginPasskeyRegistration(t, srv, alice.ID).PublicKey.ExcludeCredentials

	want := b64url.EncodeToString(a.credID)
	if len(excluded) != 1 || excluded[0].ID != want || excluded[0].Type != "public-key" {
		t.Fatalf("excludeCredentials = %+v, want only alice's passkey {Type:public-key ID:%s}", excluded, want)
	}
}

// A passkey registered through Sentinel's handlers signs in through them.
func TestPasskeyRegisterAndLogin(t *testing.T) {
	srv, db := newPasskeyTestServer(t, true)
	user := createPasskeyUser(t, db, "alice")
	a := newSoftAuthenticator(t)

	registerPasskey(t, srv, user.ID, a)

	creds, err := db.ListWebAuthnCredentialsForUser(user.ID)
	if err != nil || len(creds) != 1 || !bytes.Equal(creds[0].ID, a.credID) {
		t.Fatalf("stored credentials = %+v (%v), want the one registered", creds, err)
	}
	stored, _ := db.GetUser(user.ID)
	if len(stored.WebAuthnUserID) == 0 {
		t.Fatal("registration saved no user handle")
	}

	assertPasskeySignIn(t, srv, loginWithPasskey(t, srv, a, stored.WebAuthnUserID), user.ID)
}

// passkeyFixture is a passkey registered and stored by an earlier release.
type passkeyFixture struct {
	Library      string `json:"library"`
	RPID         string `json:"rp_id"`
	Origin       string `json:"origin"`
	Username     string `json:"username"`
	UserID       string `json:"user_id"`
	UserHandle   string `json:"user_handle"`
	StoredRecord string `json:"stored_record"` // the database value, byte for byte
	PrivPKCS8    string `json:"priv_pkcs8"`
}

// A credential record as v3.0 (go-webauthn v0.15.0) wrote it to the database
// still signs in after the library update.
func TestPasskeyStoredRecordFromEarlierReleaseLogsIn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "passkey_record_v0.15.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx passkeyFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if fx.RPID != passkeyTestRPID || fx.Origin != passkeyTestOrigin {
		t.Fatalf("fixture is for %s at %s, want %s at %s", fx.RPID, fx.Origin, passkeyTestRPID, passkeyTestOrigin)
	}
	handle, err := b64url.DecodeString(fx.UserHandle)
	if err != nil {
		t.Fatal(err)
	}
	der, err := b64url.DecodeString(fx.PrivPKCS8)
	if err != nil {
		t.Fatal(err)
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("fixture key is %T, want ECDSA", k)
	}

	srv, db := newPasskeyTestServer(t, true)
	if err := db.CreateUser(auth.User{
		ID: fx.UserID, Username: fx.Username, RoleID: auth.RoleAdminID, WebAuthnUserID: handle,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	var cred auth.WebAuthnCredential
	if err := json.Unmarshal([]byte(fx.StoredRecord), &cred); err != nil {
		t.Fatalf("decode stored record: %v", err)
	}
	// The store writes json.Marshal of this struct, so the database holds the
	// old record's exact bytes only while the struct reads and writes it unchanged.
	if again, _ := json.Marshal(cred); string(again) != fx.StoredRecord {
		t.Fatalf("stored record does not round-trip:\n got %s\nwant %s", again, fx.StoredRecord)
	}
	if err := db.CreateWebAuthnCredential(cred); err != nil {
		t.Fatalf("CreateWebAuthnCredential: %v", err)
	}

	assertPasskeySignIn(t, srv, loginWithPasskey(t, srv, &softAuthenticator{credID: cred.ID, key: key}, handle), fx.UserID)

	// The same credential signed with any other key is refused by the
	// signature check, so the pass above used the stored public key. The
	// message tells that check apart from the later 401 for a refused session.
	other := newSoftAuthenticator(t)
	other.credID = cred.ID
	w := loginWithPasskey(t, srv, other, handle)
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); w.Code != http.StatusUnauthorized || err != nil || resp["error"] != "passkey authentication failed" {
		t.Errorf("login with the wrong key: status = %d body = %s, want %d with error %q",
			w.Code, w.Body.String(), http.StatusUnauthorized, "passkey authentication failed")
	}
}

// With passkeys off (SetWebAuthn never called), every passkey endpoint
// answers with a JSON error, even when a ceremony and a stored passkey exist
// that would otherwise lead it into the WebAuthn library.
func TestPasskeyEndpointsWhenDisabled(t *testing.T) {
	srv, db := newPasskeyTestServer(t, false)
	user := createPasskeyUser(t, db, "alice")
	a := newSoftAuthenticator(t)
	if err := db.CreateWebAuthnCredential(auth.WebAuthnCredential{ID: a.credID, UserID: user.ID, Name: "Passkey"}); err != nil {
		t.Fatalf("CreateWebAuthnCredential: %v", err)
	}
	srv.deps.Auth.Ceremonies.Put("register::"+user.ID, &webauthn.SessionData{}, user.ID)
	srv.deps.Auth.Ceremonies.Put("login::ceremony", &webauthn.SessionData{}, "")

	credPath := "/api/auth/passkeys/" + b64url.EncodeToString(a.credID)
	tests := []struct {
		name    string
		method  string
		path    string
		handler http.HandlerFunc
	}{
		{"register begin", http.MethodPost, "/api/auth/passkeys/register/begin", srv.apiPasskeyRegisterBegin},
		{"register finish", http.MethodPost, "/api/auth/passkeys/register/finish", srv.apiPasskeyRegisterFinish},
		{"login begin", http.MethodPost, "/api/auth/passkeys/login/begin", srv.apiPasskeyLoginBegin},
		{"login finish", http.MethodPost, "/api/auth/passkeys/login/finish", srv.apiPasskeyLoginFinish},
		{"delete", http.MethodDelete, credPath, srv.apiDeletePasskey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := reqWithAuthContext(httptest.NewRequest(tt.method, tt.path, strings.NewReader("{}")), &user)
			r.AddCookie(&http.Cookie{Name: "sentinel_webauthn_session", Value: "ceremony"})
			r.SetPathValue("id", b64url.EncodeToString(a.credID))
			w := httptest.NewRecorder()
			if p := callHandler(tt.handler, w, r); p != nil {
				t.Fatalf("handler panicked: %v", p)
			}
			if w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", w.Code, http.StatusNotFound)
			}
			if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q, want JSON", ct)
			}
			var resp map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp["error"] != "passkeys not configured" {
				t.Errorf("body = %s (%v), want error %q", w.Body.String(), err, "passkeys not configured")
			}
		})
	}

	// The stored passkey stays in the database but is not offered.
	w := httptest.NewRecorder()
	srv.apiPasskeysAvailable(w, httptest.NewRequest(http.MethodGet, "/api/auth/passkeys/available", nil))
	if got := strings.TrimSpace(w.Body.String()); w.Code != http.StatusOK || got != `{"available":false,"configured":false}` {
		t.Errorf("available: status = %d body = %s, want 200 with both false", w.Code, got)
	}
	w = httptest.NewRecorder()
	srv.apiListPasskeys(w, reqWithAuthContext(httptest.NewRequest(http.MethodGet, "/api/auth/passkeys", nil), &user))
	if got := strings.TrimSpace(w.Body.String()); w.Code != http.StatusOK || got != "[]" {
		t.Errorf("list: status = %d body = %s, want 200 []", w.Code, got)
	}
}

// callHandler runs h and returns what it panicked with, if anything.
func callHandler(h http.HandlerFunc, w http.ResponseWriter, r *http.Request) (p any) {
	defer func() {
		if rec := recover(); rec != nil {
			p = fmt.Sprint(rec)
		}
	}()
	h(w, r)
	return nil
}
