package cluster

import "time"

// HostState represents the lifecycle state of a registered agent host.
type HostState string

const (
	HostActive         HostState = "active"
	HostPaused         HostState = "paused"         // no new updates, finish in-progress
	HostDecommissioned HostState = "decommissioned" // certs revoked, data GC'd
)

// HostInfo describes a registered remote agent host.
type HostInfo struct {
	ID           string    `json:"id"`          // unique host identifier (generated on enrollment)
	Name         string    `json:"name"`        // human-readable label (from agent config)
	Address      string    `json:"address"`     // gRPC address (host:port)
	State        HostState `json:"state"`       // lifecycle state
	CertSerial   string    `json:"cert_serial"` // agent certificate serial number
	EnrolledAt   time.Time `json:"enrolled_at"`
	LastSeen     time.Time `json:"last_seen"`
	AgentVersion string    `json:"agent_version"`
	Features     []string  `json:"features,omitempty"` // supported feature flags
}

// EnrollRequest is sent by an agent to register with the server.
// The agent generates a PKCS#10 CSR locally and sends it along with the
// one-time enrollment token that proves it was authorised to join.
type EnrollRequest struct {
	Token    string `json:"token"`     // one-time enrollment token
	HostName string `json:"host_name"` // human-readable label
	CSR      []byte `json:"csr"`       // PKCS#10 certificate signing request (DER)
}

// EnrollResponse is returned by the server after successful enrollment.
// Contains the signed agent cert plus the CA cert so the agent can verify
// the server's identity on subsequent connections.
type EnrollResponse struct {
	HostID    string `json:"host_id"`
	CACert    []byte `json:"ca_cert"`    // CA certificate PEM
	AgentCert []byte `json:"agent_cert"` // signed agent certificate PEM
}

// EnrollToken represents a one-time enrollment token stored on the server.
// The plaintext token is shown once at creation time; only the HMAC hash
// is persisted so a DB compromise doesn't leak valid tokens.
type EnrollToken struct {
	ID        string    `json:"id"`   // token ID (public, for revocation/lookup)
	Hash      []byte    `json:"hash"` // HMAC-SHA256 of the plaintext token value
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
}

// ContainerInfo is a simplified container representation sent over gRPC.
// Contains only the fields the server needs for update decisions — not the
// full Docker inspect response, which would be wasteful to serialise.
type ContainerInfo struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Image       string            `json:"image"`        // full image reference
	ImageDigest string            `json:"image_digest"` // current running digest
	State       string            `json:"state"`        // running, stopped, etc.
	Labels      map[string]string `json:"labels,omitempty"`
	Created     time.Time         `json:"created"`
}

// JournalEntry records an action taken by the agent while offline (or while
// the server was unreachable). When the connection is re-established, pending
// journal entries are replayed to the server so it has a complete audit trail.
type JournalEntry struct {
	ID        string        `json:"id"` // unique action ID
	Timestamp time.Time     `json:"timestamp"`
	Action    string        `json:"action"` // "update", "rollback", "hook"
	Container string        `json:"container"`
	OldImage  string        `json:"old_image,omitempty"`
	NewImage  string        `json:"new_image,omitempty"`
	OldDigest string        `json:"old_digest,omitempty"`
	NewDigest string        `json:"new_digest,omitempty"`
	Outcome   string        `json:"outcome"` // "success", "failed", "rollback"
	Error     string        `json:"error,omitempty"`
	Duration  time.Duration `json:"duration"`
}
