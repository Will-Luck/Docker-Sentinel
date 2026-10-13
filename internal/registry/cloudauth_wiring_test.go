package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry/cloudauth"
)

type fakeCloudProvider struct{ calls int }

func (f *fakeCloudProvider) Name() string { return "ecr" }
func (f *fakeCloudProvider) Matches(host string) bool {
	return host == "123456789012.dkr.ecr.eu-west-1.amazonaws.com"
}
func (f *fakeCloudProvider) GetCredentials(context.Context) (string, string, time.Time, error) {
	f.calls++
	return "AWS", "ecr-token", time.Now().Add(time.Hour), nil
}

const ecrImage = "123456789012.dkr.ecr.eu-west-1.amazonaws.com/app:1.2.3"

// Cloud registries (ECR, ACR, GCR) authenticate through their provider, with no
// credential stored by hand. A stored credential for the host still wins.
func TestCheckerUsesCloudProviderForCloudRegistries(t *testing.T) {
	prov := &fakeCloudProvider{}
	cloud := cloudauth.New()
	cloud.AddProvider(prov)

	c := NewChecker(newMockRegistry(), logging.New(false))
	c.SetCloudAuth(cloud)

	cred := c.credentialFor(context.Background(), "123456789012.dkr.ecr.eu-west-1.amazonaws.com")
	if cred == nil || cred.Username != "AWS" || cred.Secret != "ecr-token" {
		t.Fatalf("cloud credential = %+v", cred)
	}
	if got := c.credentialFor(context.Background(), "ghcr.io"); got != nil {
		t.Errorf("a non-cloud host got a credential: %+v", got)
	}

	// The same credential authorises the daemon's pull.
	raw, err := base64.URLEncoding.DecodeString(c.EncodedAuth(ecrImage))
	if err != nil {
		t.Fatalf("EncodedAuth: %v", err)
	}
	var got map[string]string
	_ = json.Unmarshal(raw, &got)
	if got["username"] != "AWS" || got["password"] != "ecr-token" || got["serveraddress"] != "123456789012.dkr.ecr.eu-west-1.amazonaws.com" {
		t.Errorf("encoded auth = %v", got)
	}
}
