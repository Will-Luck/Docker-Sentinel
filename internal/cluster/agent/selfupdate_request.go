package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/selfupdate"
)

// selfUpdateRequestFile keeps, in the data directory the replacement
// shares, the request of a self-update whose outcome the hub waits for.
const selfUpdateRequestFile = "self-update-request.json"

// selfUpdateRequest is a self-update the hub waits for the outcome of: the
// agent that comes back reports it under RequestID.
type selfUpdateRequest struct {
	RequestID string    `json:"request_id"`
	Container string    `json:"container"`
	OldImage  string    `json:"old_image"`
	OldDigest string    `json:"old_digest,omitempty"`
	NewImage  string    `json:"new_image"`
	NewDigest string    `json:"new_digest,omitempty"`
	Started   time.Time `json:"started"`
}

func (a *Agent) selfUpdateRequestPath() string {
	if a.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(a.cfg.DataDir, selfUpdateRequestFile)
}

// awaitedSelfUpdate keeps the request of a self-update the helper has taken
// over (its result r), for the agent that comes back to report the outcome
// under, when the hub waits for it (FeatureSelfUpdatePending). It reports
// whether it does; an older hub, or a request that cannot be kept, is told
// "success" as before.
func (a *Agent) awaitedSelfUpdate(requestID string, r *proto.UpdateResult, start time.Time) bool {
	path := a.selfUpdateRequestPath()
	if path == "" {
		return false
	}
	if !a.hubPending.Load() || requestID == "" {
		_ = os.Remove(path) // an earlier request's, which would be mistaken for this one
		return false
	}
	data, err := json.Marshal(selfUpdateRequest{
		RequestID: requestID, Container: r.GetContainerName(),
		OldImage: r.GetOldImage(), OldDigest: r.GetOldDigest(), NewImage: r.GetNewImage(), NewDigest: r.GetNewDigest(),
		Started: start,
	})
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		a.log.Warn("could not keep the self-update request; the hub is told it succeeded, as before v3.0.0", "error", err)
		_ = os.Remove(path)
		return false
	}
	return true
}

// takeSelfUpdateRequest returns, and forgets, the request of the self-update
// that tried newImage ("" when not known), if the hub waits for its outcome.
func (a *Agent) takeSelfUpdateRequest(newImage string) *selfUpdateRequest {
	path := a.selfUpdateRequestPath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			a.log.Warn("self-update request unreadable", "error", err)
		}
		return nil
	}
	_ = os.Remove(path)
	var req selfUpdateRequest
	if err := json.Unmarshal(data, &req); err != nil {
		a.log.Warn("self-update request is corrupt, ignored", "error", err)
		return nil
	}
	if newImage != "" && req.NewImage != "" && req.NewImage != newImage {
		return nil // an earlier request's
	}
	return &req
}

// selfUpdateStayed reports a self-update that ended while this agent kept
// running (the helper did not replace it) to a hub that waits for its
// outcome: the outcome the helper left, or waitErr.
func (a *Agent) selfUpdateStayed(waitErr error) {
	path := a.selfUpdateRequestPath()
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	out := selfupdate.ReportOutcome(a.cfg.DataDir, a.log)
	if out == nil {
		out = &selfupdate.Outcome{Result: selfupdate.ResultFailed, Error: waitErr.Error(), FinishedAt: time.Now()}
	}
	a.reportSelfUpdateOutcome(out)
	a.flushJournal()
}
