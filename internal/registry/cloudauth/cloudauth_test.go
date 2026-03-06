package cloudauth

import (
	"context"
	"testing"
	"time"
)

func TestRegistryMatchesECR(t *testing.T) {
	ecr := NewECR(ECRConfig{Region: "us-east-1"})
	tests := []struct {
		host string
		want bool
	}{
		{"123456789012.dkr.ecr.us-east-1.amazonaws.com", true},
		{"999999999.dkr.ecr.eu-west-1.amazonaws.com", true},
		{"ghcr.io", false},
		{"docker.io", false},
	}
	for _, tt := range tests {
		if got := ecr.Matches(tt.host); got != tt.want {
			t.Errorf("ECR.Matches(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestRegistryMatchesACR(t *testing.T) {
	acr := NewACR(ACRConfig{LoginServer: "myregistry.azurecr.io"})
	tests := []struct {
		host string
		want bool
	}{
		{"myregistry.azurecr.io", true},
		{"other.azurecr.io", true},
		{"docker.io", false},
		{"ghcr.io", false},
	}
	for _, tt := range tests {
		if got := acr.Matches(tt.host); got != tt.want {
			t.Errorf("ACR.Matches(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestRegistryMatchesGCR(t *testing.T) {
	gcr := NewGCR(GCRConfig{ServiceAccountJSON: "{}"})
	tests := []struct {
		host string
		want bool
	}{
		{"gcr.io", true},
		{"us.gcr.io", true},
		{"eu.gcr.io", true},
		{"asia.gcr.io", true},
		{"us-docker.pkg.dev", true},
		{"europe-docker.pkg.dev", true},
		{"docker.io", false},
		{"ghcr.io", false},
	}
	for _, tt := range tests {
		if got := gcr.Matches(tt.host); got != tt.want {
			t.Errorf("GCR.Matches(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
}

func TestGCRCredentials(t *testing.T) {
	gcr := NewGCR(GCRConfig{ServiceAccountJSON: `{"type":"service_account"}`})
	u, p, expiry, err := gcr.GetCredentials(context.Background())
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if u != "_json_key" {
		t.Errorf("username = %q, want _json_key", u)
	}
	if p != `{"type":"service_account"}` {
		t.Errorf("password = %q, want service account JSON", p)
	}
	if expiry.Before(time.Now()) {
		t.Error("expiry is in the past")
	}
}

func TestRegistryCacheHit(t *testing.T) {
	reg := New()
	reg.AddProvider(NewGCR(GCRConfig{ServiceAccountJSON: "{}"}))

	// First call should work.
	u1, _, err := reg.GetCredentials(context.Background(), "gcr.io")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if u1 != "_json_key" {
		t.Errorf("got %q, want _json_key", u1)
	}

	// Second call should hit cache.
	u2, _, err := reg.GetCredentials(context.Background(), "gcr.io")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if u2 != "_json_key" {
		t.Errorf("got %q, want _json_key", u2)
	}
}

func TestRegistryNoMatch(t *testing.T) {
	reg := New()
	reg.AddProvider(NewGCR(GCRConfig{ServiceAccountJSON: "{}"}))

	u, p, err := reg.GetCredentials(context.Background(), "docker.io")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u != "" || p != "" {
		t.Errorf("got u=%q p=%q, want empty", u, p)
	}
}

func TestRegistryClearCache(t *testing.T) {
	reg := New()
	reg.AddProvider(NewGCR(GCRConfig{ServiceAccountJSON: "{}"}))

	_, _, _ = reg.GetCredentials(context.Background(), "gcr.io")
	reg.ClearCache()

	// After clear, cache should be empty but still work.
	u, _, err := reg.GetCredentials(context.Background(), "gcr.io")
	if err != nil {
		t.Fatalf("after clear: %v", err)
	}
	if u != "_json_key" {
		t.Errorf("got %q, want _json_key", u)
	}
}
