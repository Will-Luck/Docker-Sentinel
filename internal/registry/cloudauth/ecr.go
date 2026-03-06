package cloudauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ECRConfig holds AWS ECR authentication configuration.
type ECRConfig struct {
	Region    string `json:"region"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	AccountID string `json:"account_id"` // optional, for host matching
}

type ecrProvider struct {
	cfg ECRConfig
}

func NewECR(cfg ECRConfig) Provider {
	return &ecrProvider{cfg: cfg}
}

func (p *ecrProvider) Name() string { return "ecr" }

func (p *ecrProvider) Matches(host string) bool {
	// ECR hosts look like: 123456789012.dkr.ecr.us-east-1.amazonaws.com
	return strings.HasSuffix(host, ".amazonaws.com") && strings.Contains(host, ".dkr.ecr.")
}

func (p *ecrProvider) GetCredentials(ctx context.Context) (string, string, time.Time, error) {
	// AWS ECR GetAuthorizationToken via HTTP + SigV4.
	now := time.Now().UTC()
	endpoint := fmt.Sprintf("https://api.ecr.%s.amazonaws.com/", p.cfg.Region)

	body := "{}"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(body))
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("create ECR request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonEC2ContainerRegistry_V20150921.GetAuthorizationToken")
	req.Header.Set("Host", fmt.Sprintf("api.ecr.%s.amazonaws.com", p.cfg.Region))

	// Sign with AWS Signature V4.
	signAWSRequest(req, body, p.cfg.Region, "ecr", p.cfg.AccessKey, p.cfg.SecretKey, now)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("ECR request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", "", time.Time{}, fmt.Errorf("ECR returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		AuthorizationData []struct {
			AuthorizationToken string  `json:"authorizationToken"`
			ExpiresAt          float64 `json:"expiresAt"` // Unix timestamp
		} `json:"authorizationData"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", time.Time{}, fmt.Errorf("decode ECR response: %w", err)
	}
	if len(result.AuthorizationData) == 0 {
		return "", "", time.Time{}, fmt.Errorf("no authorization data in ECR response")
	}

	decoded, err := base64.StdEncoding.DecodeString(result.AuthorizationData[0].AuthorizationToken)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("decode ECR token: %w", err)
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", "", time.Time{}, fmt.Errorf("unexpected ECR token format")
	}

	expiry := time.Unix(int64(result.AuthorizationData[0].ExpiresAt), 0)
	return parts[0], parts[1], expiry, nil
}

// signAWSRequest adds AWS Signature V4 headers to an HTTP request.
func signAWSRequest(req *http.Request, body, region, service, accessKey, secretKey string, now time.Time) {
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	req.Header.Set("X-Amz-Date", amzDate)

	// Create canonical request.
	payloadHash := sha256Hex([]byte(body))
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-amz-date:%s\nx-amz-target:%s\n",
		req.Header.Get("Content-Type"),
		req.Header.Get("Host"),
		amzDate,
		req.Header.Get("X-Amz-Target"),
	)
	signedHeaders := "content-type;host;x-amz-date;x-amz-target"

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		"POST",
		"/",
		"",
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	)

	// Create string to sign.
	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, region, service)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	)

	// Calculate signing key.
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))

	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", authHeader)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
