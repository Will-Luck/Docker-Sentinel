package store

import (
	"path/filepath"
	"testing"
	"time"
)

// A self-update replacement starts while the previous instance is still
// shutting down and holding the database lock. It must wait for the lock for
// longer than the normal five seconds' worth of patience would suggest.
func TestOpenWaitsForPreviousInstanceWhenSelfUpdateReplacement(t *testing.T) {
	t.Setenv(SelfUpdateReplacementEnv, "1")
	path := filepath.Join(t.TempDir(), "sentinel.db")

	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(6 * time.Second)
		_ = first.Close()
	}()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("replacement gave up waiting for the lock: %v", err)
	}
	_ = second.Close()
}

func TestOpenFailsFastWithoutSelfUpdateMarker(t *testing.T) {
	t.Setenv(SelfUpdateReplacementEnv, "")
	path := filepath.Join(t.TempDir(), "sentinel.db")

	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	start := time.Now()
	if second, err := Open(path); err == nil {
		_ = second.Close()
		t.Fatal("second Open succeeded while the lock was held")
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("Open took %s to fail, want the normal short timeout", d)
	}
}
