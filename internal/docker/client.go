package docker

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

// Client wraps the Docker API client.
type Client struct {
	api *client.Client
}

// NewClient creates a Docker client connected to the given socket or TCP endpoint.
func NewClient(dockerSock string) (*Client, error) {
	var opts []client.Opt

	switch {
	case strings.HasPrefix(dockerSock, "tcp://"), strings.HasPrefix(dockerSock, "tcps://"):
		opts = append(opts, client.WithHost(dockerSock))
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

// Close releases the Docker client resources.
func (c *Client) Close() error {
	return c.api.Close()
}
