package backup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// S3Config holds configuration for S3-compatible storage.
type S3Config struct {
	Endpoint  string `json:"endpoint"` // e.g. "s3.amazonaws.com" or "garage.local:3900"
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"` // e.g. "sentinel-backups/"
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	UseSSL    bool   `json:"use_ssl"`
	Region    string `json:"region"` // default "us-east-1"
}

// s3Uploader implements S3Uploader using stdlib HTTP with SigV4 signing.
type s3Uploader struct {
	cfg    S3Config
	client *http.Client
}

// NewS3Uploader creates an S3 uploader from the given config.
func NewS3Uploader(cfg S3Config) (S3Uploader, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("S3 endpoint and bucket are required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return &s3Uploader{
		cfg:    cfg,
		client: &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

// Upload sends a local file to the configured S3 bucket using PutObject.
// Uses path-style addressing for maximum compatibility with S3-compatible
// services (AWS, Garage, Wasabi, etc.).
func (u *s3Uploader) Upload(ctx context.Context, localPath, objectName string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	// Compute payload SHA256 by reading the file, then seek back.
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash file: %w", err)
	}
	payloadHash := hex.EncodeToString(h.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek file: %w", err)
	}

	scheme := "https"
	if !u.cfg.UseSSL {
		scheme = "http"
	}
	key := u.cfg.Prefix + objectName
	url := fmt.Sprintf("%s://%s/%s/%s", scheme, u.cfg.Endpoint, u.cfg.Bucket, key)

	req, err := http.NewRequestWithContext(ctx, "PUT", url, f)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.ContentLength = stat.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signS3Request(req, u.cfg.Region, u.cfg.AccessKey, u.cfg.SecretKey, payloadHash, time.Now().UTC())

	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("S3 request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("S3 returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// signS3Request adds AWS Signature V4 headers to an S3 PutObject request.
func signS3Request(req *http.Request, region, accessKey, secretKey, payloadHash string, now time.Time) {
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	req.Header.Set("X-Amz-Date", amzDate)

	host := req.URL.Host

	// Build canonical headers: must be sorted by key.
	headers := map[string]string{
		"content-type":         req.Header.Get("Content-Type"),
		"host":                 host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	var keys []string
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var canonicalHeaders strings.Builder
	for _, k := range keys {
		canonicalHeaders.WriteString(k)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(headers[k])
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(keys, ";")

	// Canonical URI: everything after host, before query string.
	canonicalURI := req.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	canonicalRequest := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		req.Method,
		canonicalURI,
		"", // no query string for PutObject
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	)

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, region)
	stringToSign := fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		amzDate,
		credentialScope,
		s3sha256Hex([]byte(canonicalRequest)),
	)

	kDate := s3hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := s3hmacSHA256(kDate, []byte(region))
	kService := s3hmacSHA256(kRegion, []byte("s3"))
	kSigning := s3hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(s3hmacSHA256(kSigning, []byte(stringToSign)))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
}

func s3sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func s3hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
