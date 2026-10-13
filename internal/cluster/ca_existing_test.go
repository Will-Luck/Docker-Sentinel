package cluster

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// cluster-server-13: a CA that could not be read was replaced by a new one
// without a word and its files overwritten, which locks out every enrolled
// agent for good. The old files are kept aside (and the replacement
// logged), so the CA can be restored; a CA certificate without its key is
// treated the same way.
func TestEnsureCA_KeepsACAItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name     string
		writeKey bool
	}{{"unparseable", true}, {"key missing", false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			certPath := filepath.Join(dir, "ca.pem")
			broken := []byte("not a certificate")
			if err := os.WriteFile(certPath, broken, 0600); err != nil {
				t.Fatal(err)
			}
			if tc.writeKey {
				if err := os.WriteFile(filepath.Join(dir, "ca-key.pem"), []byte("not a key"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ca, err := EnsureCA(dir)
			if err != nil || !ca.cert.IsCA {
				t.Fatalf("EnsureCA = %v, %v; want a new CA", ca, err)
			}
			kept, _ := filepath.Glob(filepath.Join(dir, "ca.pem.broken-*"))
			if len(kept) != 1 {
				t.Fatalf("kept files = %v, want the old ca.pem kept aside", kept)
			}
			if got, _ := os.ReadFile(kept[0]); !bytes.Equal(got, broken) {
				t.Error("the kept file is not the old CA certificate")
			}
		})
	}
}
