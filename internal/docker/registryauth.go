package docker

import (
	"context"
	"encoding/base64"
	"encoding/json"
)

// RegistryAuthFunc returns the encoded X-Registry-Auth value for an image
// reference, or "" when there are no credentials for its registry.
type RegistryAuthFunc func(imageRef string) string

type registryAuthKey struct{}

// WithRegistryAuth returns a context that makes pulls and digest lookups use
// the given encoded credentials instead of the client's provider. An agent
// uses it for the credentials the hub sends with an update.
func WithRegistryAuth(ctx context.Context, encoded string) context.Context {
	if encoded == "" {
		return ctx
	}
	return context.WithValue(ctx, registryAuthKey{}, encoded)
}

// SetRegistryAuth sets where the client gets registry credentials from, so a
// pull or digest lookup against a private registry is authorised by the
// credentials stored in Sentinel rather than by whatever the daemon holds.
func (c *Client) SetRegistryAuth(fn RegistryAuthFunc) { c.authFn = fn }

// registryAuth returns the credentials for ref: the context's, else the
// provider's.
func (c *Client) registryAuth(ctx context.Context, ref string) string {
	if v, ok := ctx.Value(registryAuthKey{}).(string); ok && v != "" {
		return v
	}
	if c.authFn == nil {
		return ""
	}
	return c.authFn(ref)
}

// EncodeRegistryAuth encodes credentials as the daemon expects them in the
// X-Registry-Auth header: URL-safe base64 of an AuthConfig JSON.
func EncodeRegistryAuth(username, secret, serverAddress string) string {
	b, _ := json.Marshal(map[string]string{
		"username":      username,
		"password":      secret,
		"serveraddress": serverAddress,
	})
	return base64.URLEncoding.EncodeToString(b)
}
