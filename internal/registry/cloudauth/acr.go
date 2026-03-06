package cloudauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ACRConfig holds Azure ACR authentication configuration.
type ACRConfig struct {
	TenantID     string `json:"tenant_id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	LoginServer  string `json:"login_server"` // e.g. "myregistry.azurecr.io"
}

type acrProvider struct {
	cfg ACRConfig
}

func NewACR(cfg ACRConfig) Provider {
	return &acrProvider{cfg: cfg}
}

func (p *acrProvider) Name() string { return "acr" }

func (p *acrProvider) Matches(host string) bool {
	if p.cfg.LoginServer != "" && host == p.cfg.LoginServer {
		return true
	}
	return strings.HasSuffix(host, ".azurecr.io")
}

func (p *acrProvider) GetCredentials(ctx context.Context) (string, string, time.Time, error) {
	// Step 1: Get AAD access token via client credentials grant.
	aadToken, err := p.getAADToken(ctx)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("AAD token: %w", err)
	}

	// Step 2: Exchange AAD token for ACR refresh token.
	refreshToken, err := p.exchangeForACRToken(ctx, aadToken)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("ACR exchange: %w", err)
	}

	// ACR refresh tokens are valid for ~3 hours.
	expiry := time.Now().Add(2 * time.Hour)
	return "00000000-0000-0000-0000-000000000000", refreshToken, expiry, nil
}

func (p *acrProvider) getAADToken(ctx context.Context) (string, error) {
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", p.cfg.TenantID)

	data := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"scope":         {"https://management.azure.com/.default"},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("AAD returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.AccessToken, nil
}

func (p *acrProvider) exchangeForACRToken(ctx context.Context, aadToken string) (string, error) {
	loginServer := p.cfg.LoginServer
	if loginServer == "" {
		return "", fmt.Errorf("login_server is required for ACR")
	}

	exchangeURL := fmt.Sprintf("https://%s/oauth2/exchange", loginServer)
	data := url.Values{
		"grant_type":   {"access_token"},
		"service":      {loginServer},
		"access_token": {aadToken},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", exchangeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ACR exchange returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.RefreshToken, nil
}
