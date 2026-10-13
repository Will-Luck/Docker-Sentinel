package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

const testIssuer = "https://idp.example"

func oidcInfo(sub, username string) *OIDCUserInfo {
	return &OIDCUserInfo{Issuer: testIssuer, Subject: sub, Username: username}
}

func loginOIDC(s *Service, info *OIDCUserInfo, autoCreate bool) (*Session, error) {
	return s.LoginWithOIDC(context.Background(), info, autoCreate, RoleViewerID, nil, "10.0.0.9", "ua")
}

func mustGet(t *testing.T, s *Service, id string) User {
	t.Helper()
	u, err := s.Users.GetUser(id)
	if err != nil || u == nil {
		t.Fatalf("user %s missing: %v", id, err)
	}
	return *u
}

// A local account with a password is never taken over by an IdP identity
// that merely claims the same username.
func TestOIDC_LocalPasswordAccountIsNeverAutoLinked(t *testing.T) {
	svc := newTestService(true)
	hash, _ := HashPassword("correct-horse-battery")
	_ = svc.Users.CreateUser(User{ID: "local-admin", Username: "admin", PasswordHash: hash, RoleID: RoleAdminID})

	sess, err := loginOIDC(svc, oidcInfo("attacker-sub", "admin"), true)
	if !errors.Is(err, ErrOIDCAccountExists) || sess != nil {
		t.Fatalf("login = (%v, %v), want ErrOIDCAccountExists and no session", sess, err)
	}
	if u := mustGet(t, svc, "local-admin"); u.OIDCSubject != "" {
		t.Errorf("local admin was linked to %q", u.OIDCSubject)
	}
}

func TestOIDC_AutoCreateBindsIssuerAndSubject(t *testing.T) {
	svc := newTestService(true)
	sess, err := loginOIDC(svc, oidcInfo("sub-1", "carol"), true)
	if err != nil || sess == nil {
		t.Fatalf("auto-create login: %v", err)
	}
	u := mustGet(t, svc, sess.UserID)
	if u.OIDCIssuer != testIssuer || u.OIDCSubject != "sub-1" {
		t.Errorf("created user = %+v, want bound to %s/sub-1 with no local password", u, testIssuer)
	}
	// The same identity logs in again even after the IdP renamed the user.
	sess2, err := loginOIDC(svc, oidcInfo("sub-1", "carol-renamed"), false)
	if err != nil || sess2.UserID != sess.UserID {
		t.Errorf("renamed identity login = (%v, %v), want the same account", sess2, err)
	}
}

func TestOIDC_MatchesOnIssuerAndSubjectOnly(t *testing.T) {
	svc := newTestService(true)
	sess, _ := loginOIDC(svc, oidcInfo("sub-1", "carol"), true)

	// Same username, different subject: someone else.
	if _, err := loginOIDC(svc, oidcInfo("sub-2", "carol"), true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("different subject, same username = %v, want ErrOIDCAccountExists", err)
	}
	// Same subject under another issuer: someone else.
	other := &OIDCUserInfo{Issuer: "https://other.example", Subject: "sub-1", Username: "carol"}
	if _, err := loginOIDC(svc, other, true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("different issuer, same subject = %v, want ErrOIDCAccountExists", err)
	}
	if u := mustGet(t, svc, sess.UserID); u.OIDCSubject != "sub-1" {
		t.Errorf("bound subject changed to %q", u.OIDCSubject)
	}
}

func TestOIDC_RefusesMissingIssuerOrSubject(t *testing.T) {
	svc := newTestService(true)
	for _, info := range []*OIDCUserInfo{
		{Issuer: "", Subject: "s", Username: "x"},
		{Issuer: testIssuer, Subject: "", Username: "x"},
	} {
		if _, err := loginOIDC(svc, info, true); err == nil {
			t.Errorf("login with %+v succeeded", info)
		}
	}
}

func TestOIDC_UnknownUserWithAutoCreateOffIsRefused(t *testing.T) {
	svc := newTestService(true)
	if _, err := loginOIDC(svc, oidcInfo("s", "nobody"), false); err == nil {
		t.Error("unknown identity logged in with auto-create off")
	}
}

// Nothing links by username on its own: a clash with an unlinked account is
// refused, whoever the account is.
func TestOIDC_UnlinkedAccountIsNeverLinkedByDefault(t *testing.T) {
	svc := newTestService(true)
	_ = svc.Users.CreateUser(User{ID: "legacy", Username: "dave", PasswordHash: "x", RoleID: RoleViewerID})
	if _, err := loginOIDC(svc, oidcInfo("dave-sub", "dave"), true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Fatalf("login onto an unlinked account = %v, want ErrOIDCAccountExists", err)
	}
	if u := mustGet(t, svc, "legacy"); u.OIDCSubject != "" {
		t.Errorf("account was linked: %+v", u)
	}
}

func TestOIDC_ErrorSaysHowToLink(t *testing.T) {
	want := "This account is not linked to your sign-in provider yet. Ask an admin to allow linking, or sign in with your password and link it from your Account page."
	if ErrOIDCAccountExists.Error() != want {
		t.Errorf("message = %q", ErrOIDCAccountExists.Error())
	}
}

// An admin can allow the next SSO login for one user to link. It links once
// and the flag is cleared; both steps are audited.
func TestOIDC_AdminAllowedLinkWorksOnce(t *testing.T) {
	svc := newTestService(true)
	var audit []string
	svc.AuditHook = func(msg string) { audit = append(audit, msg) }
	_ = svc.Users.CreateUser(User{ID: "a", Username: "alice", PasswordHash: "x", RoleID: RoleAdminID})

	if err := svc.AllowOIDCLink("a"); err != nil {
		t.Fatal(err)
	}
	u := mustGet(t, svc, "a")
	if until := time.Until(u.OIDCLinkUntil); until < 23*time.Hour || until > 25*time.Hour {
		t.Errorf("link window = %v, want about 24h", until)
	}
	// A different username does not use the window.
	if _, err := loginOIDC(svc, oidcInfo("other", "someone-else"), false); err == nil {
		t.Error("an unrelated login succeeded")
	}
	sess, err := loginOIDC(svc, oidcInfo("alice-sub", "alice"), false)
	if err != nil || sess.UserID != "a" {
		t.Fatalf("allowed link login = (%v, %v)", sess, err)
	}
	u = mustGet(t, svc, "a")
	if u.OIDCSubject != "alice-sub" || !u.OIDCLinkUntil.IsZero() {
		t.Errorf("after link: %+v, want bound and the window cleared", u)
	}
	if _, err := loginOIDC(svc, oidcInfo("mallory", "alice"), true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("a second identity after the link = %v, want ErrOIDCAccountExists", err)
	}
	if len(audit) != 2 {
		t.Errorf("audit entries = %v, want one for allowing and one for linking", audit)
	}
}

func TestOIDC_ExpiredLinkWindowIsRefused(t *testing.T) {
	svc := newTestService(true)
	_ = svc.Users.CreateUser(User{ID: "a", Username: "alice", PasswordHash: "x", RoleID: RoleViewerID, OIDCLinkUntil: time.Now().Add(-time.Minute)})
	if _, err := loginOIDC(svc, oidcInfo("s", "alice"), true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("expired window = %v, want ErrOIDCAccountExists", err)
	}
}

// With the opt-in on, an unlinked non-admin account links by username. An
// admin account never does.
func TestOIDC_LinkExistingOptInSkipsAdmins(t *testing.T) {
	svc := newTestService(true)
	svc.LinkExisting = func() bool { return true }
	_ = svc.Users.CreateUser(User{ID: "op", Username: "olive", PasswordHash: "x", RoleID: RoleOperatorID})
	_ = svc.Users.CreateUser(User{ID: "ad", Username: "alice", PasswordHash: "x", RoleID: RoleAdminID})

	if sess, err := loginOIDC(svc, oidcInfo("olive-sub", "olive"), false); err != nil || sess.UserID != "op" {
		t.Errorf("non-admin with the opt-in = (%v, %v), want linked", sess, err)
	}
	if _, err := loginOIDC(svc, oidcInfo("alice-sub", "alice"), false); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("admin with the opt-in = %v, want ErrOIDCAccountExists", err)
	}
	// The opt-in off again: nothing links.
	svc.LinkExisting = func() bool { return false }
	_ = svc.Users.CreateUser(User{ID: "v", Username: "vic", PasswordHash: "x", RoleID: RoleViewerID})
	if _, err := loginOIDC(svc, oidcInfo("vic-sub", "vic"), false); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("opt-in off = %v, want ErrOIDCAccountExists", err)
	}
}

func TestLinkAndUnlinkOIDC(t *testing.T) {
	svc := newTestService(true)
	hash, _ := HashPassword("pw")
	_ = svc.Users.CreateUser(User{ID: "a", Username: "alice", PasswordHash: hash, RoleID: RoleAdminID})
	_ = svc.Users.CreateUser(User{ID: "b", Username: "bob", PasswordHash: hash, RoleID: RoleViewerID})

	if err := svc.LinkOIDC("a", oidcInfo("alice-sub", "whoever")); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := svc.LinkOIDC("b", oidcInfo("alice-sub", "whoever")); !errors.Is(err, ErrOIDCIdentityTaken) {
		t.Errorf("linking an identity bound elsewhere = %v, want ErrOIDCIdentityTaken", err)
	}
	// The linked identity now signs in as alice, whatever its username.
	sess, err := loginOIDC(svc, oidcInfo("alice-sub", "other-name"), false)
	if err != nil || sess.UserID != "a" {
		t.Errorf("linked login = (%v, %v)", sess, err)
	}
	if err := svc.UnlinkOIDC("a"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if u := mustGet(t, svc, "a"); u.OIDCSubject != "" || u.OIDCIssuer != "" {
		t.Errorf("after unlink: %+v", u)
	}
	if _, err := loginOIDC(svc, oidcInfo("alice-sub", "alice"), true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("login after unlink = %v, want ErrOIDCAccountExists", err)
	}
}

// The opt-in does not link an account that has 2FA or a passkey: its owner
// uses the per-user action or the Account page.
func TestOIDC_LinkExistingOptInSkipsAccountsWith2FA(t *testing.T) {
	svc := newTestService(true)
	svc.LinkExisting = func() bool { return true }
	_ = svc.Users.CreateUser(User{ID: "op", Username: "olive", PasswordHash: "x", RoleID: RoleOperatorID, TOTPEnabled: true})
	if _, err := loginOIDC(svc, oidcInfo("olive-sub", "olive"), false); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("operator with 2FA under the opt-in = %v, want ErrOIDCAccountExists", err)
	}
	if u := mustGet(t, svc, "op"); u.OIDCSubject != "" {
		t.Errorf("account with 2FA was linked")
	}
}

// SENTINEL_OIDC_LINK_ADMIN is a one-shot recovery route for an SSO-only admin.
func TestOIDC_LinkAdminVariableLinksOnce(t *testing.T) {
	svc := newTestService(true)
	var audit []string
	svc.AuditHook = func(m string) { audit = append(audit, m) }
	svc.LinkAdminUsername = func() string { return "alice" }
	_ = svc.Users.CreateUser(User{ID: "ad", Username: "alice", PasswordHash: "x", RoleID: RoleAdminID, TOTPEnabled: true})
	_ = svc.Users.CreateUser(User{ID: "ad2", Username: "bert", PasswordHash: "x", RoleID: RoleAdminID})

	// Another admin is not covered by the variable.
	if _, err := loginOIDC(svc, oidcInfo("bert-sub", "bert"), false); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("an admin the variable does not name = %v, want ErrOIDCAccountExists", err)
	}
	// The named admin links, whatever the role or 2FA.
	sess, err := loginOIDC(svc, oidcInfo("alice-sub", "alice"), false)
	if err != nil || sess.UserID != "ad" {
		t.Fatalf("named admin = (%v, %v), want linked", sess, err)
	}
	u := mustGet(t, svc, "ad")
	if u.OIDCSubject != "alice-sub" || !u.OIDCLinkAdminUsed {
		t.Errorf("after the link: %+v, want bound and marked", u)
	}
	if len(audit) != 1 {
		t.Errorf("audit = %v, want the link logged", audit)
	}

	// Once unlinked, the variable (still set) does not link again.
	if err := svc.UnlinkOIDC("ad"); err != nil {
		t.Fatal(err)
	}
	if _, err := loginOIDC(svc, oidcInfo("mallory", "alice"), true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("variable reused after unlink = %v, want ErrOIDCAccountExists", err)
	}
}
