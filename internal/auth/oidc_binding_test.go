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
	_ = svc.Users.CreateUser(User{ID: "local-admin", Username: "admin", PasswordHash: hash, RoleID: RoleAdminID, LocalPassword: true})

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
	if u.OIDCIssuer != testIssuer || u.OIDCSubject != "sub-1" || u.LocalPassword {
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

// A v2.16 account created by SSO has no stored subject and no marker. It is
// linked by username once, on its next SSO login, if nothing local protects it.
func TestOIDC_LegacyAccountLinksOnceByUsername(t *testing.T) {
	svc := newTestService(true)
	_ = svc.Users.CreateUser(User{ID: "legacy", Username: "dave", PasswordHash: "x", RoleID: RoleViewerID, CreatedAt: time.Now()})

	sess, err := loginOIDC(svc, oidcInfo("dave-sub", "dave"), false)
	if err != nil || sess.UserID != "legacy" {
		t.Fatalf("legacy link login = (%v, %v)", sess, err)
	}
	if u := mustGet(t, svc, "legacy"); u.OIDCSubject != "dave-sub" || u.OIDCIssuer != testIssuer {
		t.Errorf("legacy account not bound: %+v", u)
	}
	// Linked once: a different identity with the same username is refused.
	if _, err := loginOIDC(svc, oidcInfo("mallory-sub", "dave"), true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("second identity on a linked account = %v, want ErrOIDCAccountExists", err)
	}
}

func TestOIDC_LegacyAccountWithTOTPOrPasskeyIsNotAutoLinked(t *testing.T) {
	svc := newTestService(true)
	_ = svc.Users.CreateUser(User{ID: "legacy", Username: "dave", PasswordHash: "x", RoleID: RoleAdminID, TOTPEnabled: true})
	if _, err := loginOIDC(svc, oidcInfo("s", "dave"), true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("legacy account with TOTP = %v, want ErrOIDCAccountExists", err)
	}
}

// A successful local password login proves the account has a human-held
// password, so it can no longer be linked by username.
func TestLogin_MarksAccountAsHavingALocalPassword(t *testing.T) {
	svc := newTestService(true)
	hash, _ := HashPassword("correct-horse-battery")
	_ = svc.Users.CreateUser(User{ID: "u", Username: "erin", PasswordHash: hash, RoleID: RoleAdminID})
	if _, _, err := svc.Login(context.Background(), "erin", "correct-horse-battery", "10.0.0.1", "ua"); err != nil {
		t.Fatal(err)
	}
	if u := mustGet(t, svc, "u"); !u.LocalPassword {
		t.Error("local login did not mark LocalPassword")
	}
	if _, err := loginOIDC(svc, oidcInfo("s", "erin"), true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("OIDC login onto a local-password account = %v", err)
	}
}

func TestLinkAndUnlinkOIDC(t *testing.T) {
	svc := newTestService(true)
	hash, _ := HashPassword("pw")
	_ = svc.Users.CreateUser(User{ID: "a", Username: "alice", PasswordHash: hash, RoleID: RoleAdminID, LocalPassword: true})
	_ = svc.Users.CreateUser(User{ID: "b", Username: "bob", PasswordHash: hash, RoleID: RoleViewerID, LocalPassword: true})

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
	if u := mustGet(t, svc, "a"); u.OIDCSubject != "" || u.OIDCIssuer != "" || !u.LocalPassword {
		t.Errorf("after unlink: %+v", u)
	}
	if _, err := loginOIDC(svc, oidcInfo("alice-sub", "alice"), true); !errors.Is(err, ErrOIDCAccountExists) {
		t.Errorf("login after unlink = %v, want ErrOIDCAccountExists", err)
	}
}
