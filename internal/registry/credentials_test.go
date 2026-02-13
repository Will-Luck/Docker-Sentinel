package registry

import "testing"

func TestMaskCredentialSecrets(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		want   string
	}{
		{"long secret", "ghp_abcdefghij", "ghp_****"},
		{"short secret", "ab", "****"},
		{"empty secret", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []RegistryCredential{
				{ID: "1", Registry: "ghcr.io", Username: "user", Secret: tt.secret},
			}
			masked := MaskCredentialSecrets(input)
			if masked[0].Secret != tt.want {
				t.Errorf("MaskCredentialSecrets() secret = %q, want %q", masked[0].Secret, tt.want)
			}
			// Verify original is not mutated.
			if input[0].Secret != tt.secret {
				t.Errorf("original credential was mutated: got %q, want %q", input[0].Secret, tt.secret)
			}
		})
	}
}

func TestRestoreCredentialSecrets(t *testing.T) {
	saved := []RegistryCredential{
		{ID: "1", Registry: "ghcr.io", Username: "user", Secret: "ghp_abcdefghij"},
	}

	t.Run("masked secret restored from saved", func(t *testing.T) {
		incoming := []RegistryCredential{
			{ID: "1", Registry: "ghcr.io", Username: "user", Secret: "ghp_****"},
		}
		result := RestoreCredentialSecrets(incoming, saved)
		if result[0].Secret != "ghp_abcdefghij" {
			t.Errorf("expected restored secret %q, got %q", "ghp_abcdefghij", result[0].Secret)
		}
	})

	t.Run("new plaintext secret kept as-is", func(t *testing.T) {
		incoming := []RegistryCredential{
			{ID: "2", Registry: "docker.io", Username: "other", Secret: "dckr_newtoken123"},
		}
		result := RestoreCredentialSecrets(incoming, saved)
		if result[0].Secret != "dckr_newtoken123" {
			t.Errorf("expected new secret %q, got %q", "dckr_newtoken123", result[0].Secret)
		}
	})

	t.Run("masked secret for unknown ID kept masked", func(t *testing.T) {
		incoming := []RegistryCredential{
			{ID: "3", Registry: "lscr.io", Username: "someone", Secret: "lscr****"},
		}
		result := RestoreCredentialSecrets(incoming, saved)
		if result[0].Secret != "lscr****" {
			t.Errorf("expected masked secret %q, got %q", "lscr****", result[0].Secret)
		}
	})
}

func TestFindByRegistry(t *testing.T) {
	creds := []RegistryCredential{
		{ID: "1", Registry: "docker.io", Username: "dockeruser", Secret: "secret1"},
		{ID: "2", Registry: "ghcr.io", Username: "ghuser", Secret: "secret2"},
	}

	tests := []struct {
		name     string
		registry string
		wantNil  bool
		wantID   string
	}{
		{"found docker.io", "docker.io", false, "1"},
		{"found ghcr.io", "ghcr.io", false, "2"},
		{"unknown registry returns nil", "unknown.io", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindByRegistry(creds, tt.registry)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil credential, got nil")
			}
			if got.ID != tt.wantID {
				t.Errorf("expected ID %q, got %q", tt.wantID, got.ID)
			}
		})
	}
}
