package docker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

// Client wraps the Docker API client.
type Client struct {
	api *client.Client
}

// TLSConfig holds paths to TLS certificates for connecting to a Docker
// socket proxy or remote Docker daemon over mTLS.
type TLSConfig struct {
	CACert     string // path to CA certificate file
	ClientCert string // path to client certificate file
	ClientKey  string // path to client private key file
}

// loadTLS reads the certificate files and returns a configured tls.Config.
func (t *TLSConfig) loadTLS() (*tls.Config, error) {
	caCert, err := os.ReadFile(t.CACert)
	if err != nil {
		return nil, fmt.Errorf("read CA cert %s: %w", t.CACert, err)
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA cert %s", t.CACert)
	}

	clientCert, err := tls.LoadX509KeyPair(t.ClientCert, t.ClientKey)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}

	return &tls.Config{
		RootCAs:      certPool,
		Certificates: []tls.Certificate{clientCert},
		MinVersion:   tls.VersionTLS12,
	}, nil // ServerName is set by the caller with the parsed host
}

// NewClient creates a Docker client connected to the given socket or TCP endpoint.
// If tlsCfg is non-nil and all fields are populated, mTLS is configured for TCP connections.
func NewClient(dockerSock string, tlsCfg *TLSConfig) (*Client, error) {
	var opts []client.Opt

	switch {
	case strings.HasPrefix(dockerSock, "tcp://"), strings.HasPrefix(dockerSock, "tcps://"):
		opts = append(opts, client.WithHost(dockerSock))

		// Configure TLS if certificates are provided.
		if tlsCfg != nil && tlsCfg.CACert != "" && tlsCfg.ClientCert != "" && tlsCfg.ClientKey != "" {
			tlsConfig, err := tlsCfg.loadTLS()
			if err != nil {
				return nil, fmt.Errorf("configure Docker TLS: %w", err)
			}
			// Set ServerName for proper hostname verification.
			if u, parseErr := url.Parse(dockerSock); parseErr == nil {
				tlsConfig.ServerName = u.Hostname()
			}
			opts = append(opts, client.WithHTTPClient(&http.Client{
				Transport: &http.Transport{
					TLSClientConfig:       tlsConfig,
					IdleConnTimeout:       90 * time.Second,
					TLSHandshakeTimeout:   10 * time.Second,
					ResponseHeaderTimeout: 30 * time.Second,
				},
			}))
		}
	default:
		opts = append(opts,
			client.WithHost("unix://"+dockerSock),
			client.WithHTTPClient(&http.Client{
				Transport: &http.Transport{
					DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
						return net.DialTimeout("unix", dockerSock, 30*time.Second)
					},
				},
			}),
		)
	}

	api, err := client.New(opts...)
	if err != nil {
		return nil, err
	}

	return &Client{api: api}, nil
}

// Ping checks that the Docker daemon is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.api.Ping(ctx, client.PingOptions{})
	return err
}

// Close releases the Docker client resources.
func (c *Client) Close() error {
	return c.api.Close()
}
