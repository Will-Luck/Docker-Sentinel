package store

import (
	"encoding/json"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/Will-Luck/Docker-Sentinel/internal/auth"
)

func TestDeleteUserRemovesPasskeys(t *testing.T) {
	s := testStoreWithAuth(t)
	user := auth.User{
		ID: "u1", Username: "alice", PasswordHash: "h", RoleID: "admin",
		WebAuthnUserID: []byte("handle-for-alice-0123456789012345678901234567890123456789012345"),
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWebAuthnCredential(auth.WebAuthnCredential{ID: []byte("c1"), UserID: "u1", Name: "key", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser("u1"); err != nil {
		t.Fatal(err)
	}
	any, err := s.AnyWebAuthnCredentialsExist()
	if err != nil {
		t.Fatal(err)
	}
	if any {
		t.Error("passkeys are still counted after the user was deleted")
	}
	if _, err := s.GetWebAuthnCredential([]byte("c1")); err == nil {
		t.Error("credential record survived DeleteUser")
	}
	err = s.db.View(func(tx *bolt.Tx) error {
		n := 0
		_ = tx.Bucket(bucketWebAuthnCreds).ForEach(func(k, v []byte) error { n++; return nil })
		if n != 0 {
			t.Errorf("%d webauthn keys left behind", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLegacyNotificationChannelIDsAreStable(t *testing.T) {
	s := testStore(t)
	raw, _ := json.Marshal(NotificationConfig{GotifyURL: "http://g", GotifyToken: "t", WebhookURL: "http://w"})
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSettings).Put([]byte("notification_config"), raw)
	}); err != nil {
		t.Fatal(err)
	}
	first, err := s.GetNotificationChannels()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := s.GetNotificationChannels()
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("channels: %d, %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("channel %d ID changed between reads: %q vs %q", i, first[i].ID, second[i].ID)
		}
	}
}
