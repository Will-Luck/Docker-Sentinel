package cluster

import (
	"errors"
	"time"
)

// UnansweredError is returned for an update or rollback the agent did not
// answer: its stream dropped, or the hub stopped waiting. The agent may
// still have carried it out. A caller that records the request as failed
// does so at RecordAt (see HistoryTime): the agent's result, if it comes
// later (late, or replayed from its offline journal on reconnecting), is
// recorded at the same time and replaces that row.
type UnansweredError struct {
	HostID    string
	RequestID string
	RecordAt  time.Time
	Err       error
}

func (e *UnansweredError) Error() string { return e.Err.Error() }
func (e *UnansweredError) Unwrap() error { return e.Err }

// Unanswered reports whether err is an UnansweredError.
func Unanswered(err error) bool {
	var un *UnansweredError
	return errors.As(err, &un)
}

// AnsweredError is returned for an update or rollback the agent answered
// with anything but success: it failed, or a pre-update hook asked to skip
// it. The hub has already told every open dashboard how it ended, with the
// agent's result, so a caller does not tell it again.
type AnsweredError struct {
	Err error
}

func (e *AnsweredError) Error() string { return e.Err.Error() }
func (e *AnsweredError) Unwrap() error { return e.Err }

// Answered reports whether err is an AnsweredError.
func Answered(err error) bool {
	var an *AnsweredError
	return errors.As(err, &an)
}

// HistoryTime is the time to record a failed remote request's history row
// at: for an unanswered one, the time its late result will replace it at;
// otherwise fallback.
func HistoryTime(err error, fallback time.Time) time.Time {
	var un *UnansweredError
	if errors.As(err, &un) && !un.RecordAt.IsZero() {
		return un.RecordAt
	}
	return fallback
}

// RolledBackOnRequest is the error text of the history row of a rollback
// asked for from the dashboard or the API, locally and remotely.
const RolledBackOnRequest = "rolled back on request"

// RollbackRow returns the outcome and error of the history row of a remote
// rollback that ended with the agent's outcome and errText, in the shape
// of a local rollback row: outcome "rollback" when it worked, so History
// shows the container rolled back, else "failed" with why.
func RollbackRow(outcome, errText string) (string, string) {
	if outcome == "success" {
		return "rollback", RolledBackOnRequest
	}
	if errText == "" {
		return "failed", "rollback failed"
	}
	return "failed", "rollback failed: " + errText
}

// ErrUpdateSkipped is returned for a remote update that a pre-update hook
// asked to skip (exit 75): nothing was changed. Like a local skip, it is
// neither a success nor a failure.
var ErrUpdateSkipped = errors.New("a pre-update hook asked to skip the update")

// FeatureCertRenewal is the server feature (StateAck.server_features) that
// says the server renews agent certificates safely: it keeps the old one
// valid until the agent connects with the new one.
const FeatureCertRenewal = "cert-renewal"

// FeatureUpdateSkipped is the server feature that says the server takes an
// update outcome of "skipped" (a pre-update hook exited 75) as no change.
// Servers before v3.0.0 count any outcome but "success" as a failure.
const FeatureUpdateSkipped = "update-skipped"

// FeatureSelfUpdatePending is the server feature that says the server
// takes an OutcomePending result for an agent's update of itself, and
// waits for the outcome the agent reports when it comes back. Servers
// before v3.0.0 are told "success" when the helper takes over.
const FeatureSelfUpdatePending = "self-update-pending"

// FeatureSelfUpdateOutcome is the agent feature that says it reports the
// outcome of its update of itself to a server that waits for it (agents
// since v3.0.0).
const FeatureSelfUpdateOutcome = "self-update-outcome"

// OutcomePending is the update outcome of an agent's update of itself that
// its helper has taken over: the swap is under way, and its outcome comes
// later, in the offline journal, under the request's ID.
const OutcomePending = "pending"

// FeatureKeepalive is the server feature that says the server accepts
// keepalive pings every 30 seconds. gRPC's default, which servers before
// v3.0.0 run, cuts off a client that pings more often than every five
// minutes.
const FeatureKeepalive = "keepalive"

// FeatureDiscardSwap is the agent feature (in its heartbeat and state
// report) that says it can discard a swap record on the hub's request
// (agents since v3.0.0).
const FeatureDiscardSwap = "discard-swap"

// SettingRemoveVolumesOnAgents is the hub setting ("true" or "false",
// default false) that sends its Remove volumes setting to agents too.
// Agents before v3.0.0 never removed volumes, so it is a separate choice.
const SettingRemoveVolumesOnAgents = "cluster_remove_volumes_agents"

// FeaturePullResult is the agent feature that says it answers a pull
// request with its result (agents since v3.0.0).
const FeaturePullResult = "pull-result"

// ErrPullUnconfirmed is returned for a pull sent to an agent that does not
// say how it went (agents before v3.0.0): it may or may not have worked.
var ErrPullUnconfirmed = errors.New("the agent was asked to pull the image but does not report the result (it is older than v3.0.0)")

// Errors of a remote swap record discard.
var (
	// ErrAgentTooOld: the agent cannot do what was asked (it is older than
	// the feature).
	ErrAgentTooOld = errors.New("the agent on this host is older than v3.0.0 and cannot discard swap records; update it, " +
		"or fix the containers on the host and restart the agent")
	// ErrNoSwapRecord: the agent has no swap record for the container.
	ErrNoSwapRecord = errors.New("no unfinished swap is recorded for this container on the agent")
	// ErrSwapBusy: an update or recovery of the container is running.
	ErrSwapBusy = errors.New("an update or recovery of this container is running on the agent")
)

// MaxMessageSize is the largest gRPC message the cluster server and agents
// exchange. A host with many containers and large labels sends a container
// list well over gRPC's 4 MB default.
const MaxMessageSize = 64 << 20

// HostState represents the lifecycle state of a registered agent host.
type HostState string

const (
	HostActive         HostState = "active"
	HostPaused         HostState = "paused"         // no new updates, finish in-progress
	HostDecommissioned HostState = "decommissioned" // certs revoked, data GC'd
)

// HostInfo describes a registered remote agent host.
type HostInfo struct {
	ID         string    `json:"id"`          // unique host identifier (generated on enrollment)
	Name       string    `json:"name"`        // human-readable label (from agent config)
	Address    string    `json:"address"`     // gRPC address (host:port)
	State      HostState `json:"state"`       // lifecycle state
	CertSerial string    `json:"cert_serial"` // agent certificate serial number
	// PendingCertSerial is a renewed certificate issued to the agent but
	// not yet seen in use. CertSerial stays valid until the agent connects
	// with this one.
	PendingCertSerial string    `json:"pending_cert_serial,omitempty"`
	EnrolledAt        time.Time `json:"enrolled_at"`
	LastSeen          time.Time `json:"last_seen"`
	AgentVersion      string    `json:"agent_version"`
	Features          []string  `json:"features,omitempty"`  // supported feature flags
	EngineID          string    `json:"engine_id,omitempty"` // Docker Engine ID for source dedup
	// SelfContainer is the container the agent runs in, which an update of
	// any Sentinel container on the host changes ("" when not known).
	SelfContainer string `json:"self_container,omitempty"`
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
	// UsedAt, KeyHash and HostID record the enrolment that used the token:
	// when, the SHA-256 of the CSR's public key, and the host ID issued. For
	// a while after, the same key may enrol again with the token (its first
	// answer may never have reached the agent) and gets the same host ID.
	UsedAt  time.Time `json:"used_at,omitzero"`
	KeyHash string    `json:"key_hash,omitempty"`
	HostID  string    `json:"host_id,omitempty"`
}

// PortMapping represents a single host-bound port on a container.
type PortMapping struct {
	HostIP        string `json:"host_ip,omitempty"`
	HostPort      uint16 `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
	Protocol      string `json:"protocol"`
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
	Ports       []PortMapping     `json:"ports,omitempty"`
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
