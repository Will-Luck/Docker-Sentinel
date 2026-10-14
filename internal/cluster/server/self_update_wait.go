package server

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// selfUpdateWaitBound is how long the hub waits for the outcome of an
// agent's update of itself before it records the update as failed.
var selfUpdateWaitBound = 10 * time.Minute

// errSelfUpdateNoReturn is the failure recorded when the wait ends.
const errSelfUpdateNoReturn = "the agent did not come back on the new version"

// SelfUpdateNotify sends the notification of an agent's update of itself
// once its outcome is known: hostName is its host, name the container and
// rec the history row recorded.
type SelfUpdateNotify func(hostName, name string, rec store.UpdateRecord)

// SetSelfUpdateNotify sets how the outcome of an agent's update of itself
// is notified, for the callers that notify (WithCallerRecords).
func (s *Server) SetSelfUpdateNotify(fn SelfUpdateNotify) {
	if fn == nil {
		s.selfNotify.Store(nil)
		return
	}
	s.selfNotify.Store(&fn)
}

// selfUpdateWait is an agent's update of itself that its helper took over
// (the agent answered OutcomePending): it is recorded, and notified, when
// its outcome is known.
type selfUpdateWait struct {
	hostID, requestID, name string
	// rec is the history row, without its outcome; its time is when the
	// helper took over, or when the caller recorded a failure the outcome
	// replaces.
	rec     store.UpdateRecord
	started time.Time
	notify  bool // the caller notifies its updates
	timer   *time.Timer
}

// awaitSelfUpdate starts waiting for the outcome of the self-update the
// agent on hostID answered ur (OutcomePending) to. A zero recordAt records
// the outcome at the current time.
func (s *Server) awaitSelfUpdate(hostID string, ur *proto.UpdateResult, recordAt time.Time, notify bool) {
	if recordAt.IsZero() {
		recordAt = time.Now()
	}
	w := &selfUpdateWait{
		hostID: hostID, requestID: ur.GetRequestId(), name: ur.GetContainerName(),
		rec: store.UpdateRecord{
			Timestamp:     recordAt,
			ContainerName: store.ScopedKey(hostID, ur.GetContainerName()),
			OldImage:      ur.GetOldImage(),
			OldDigest:     ur.GetOldDigest(),
			NewImage:      ur.GetNewImage(),
			NewDigest:     ur.GetNewDigest(),
			HostID:        hostID,
			HostName:      s.hostName(hostID),
		},
		started: time.Now(),
		notify:  notify,
	}
	if d := ur.GetDuration(); d != nil {
		w.started = w.started.Add(-d.AsDuration())
	}
	key := hostID + ":" + w.requestID
	s.selfMu.Lock()
	if s.selfWaits == nil {
		s.selfWaits = make(map[string]*selfUpdateWait)
	}
	s.selfWaits[key] = w
	w.timer = time.AfterFunc(selfUpdateWaitBound, func() { s.selfUpdateWaitEnded(key) })
	s.selfMu.Unlock()
	s.log.Info("agent self-update under way, waiting for its outcome", "hostID", hostID, "container", w.name, "requestID", w.requestID)
}

// takeSelfUpdate returns, and stops waiting for, the self-update that
// request requestID of hostID started.
func (s *Server) takeSelfUpdate(hostID, requestID string) *selfUpdateWait {
	s.selfMu.Lock()
	defer s.selfMu.Unlock()
	key := hostID + ":" + requestID
	w := s.selfWaits[key]
	if w != nil {
		delete(s.selfWaits, key)
		w.timer.Stop()
	}
	return w
}

// selfUpdateWaitEnded records a self-update whose outcome did not come in
// time: as done if its agent is back on the new image, else as failed.
func (s *Server) selfUpdateWaitEnded(key string) {
	s.selfMu.Lock()
	w := s.selfWaits[key]
	delete(s.selfWaits, key)
	s.selfMu.Unlock()
	if w == nil {
		return
	}
	if s.backOnTarget(w) {
		s.finishSelfUpdate(w, "success", "")
		return
	}
	s.finishSelfUpdate(w, "failed", errSelfUpdateNoReturn)
}

// backOnTarget reports whether the agent of w is connected and its
// container runs the image the update went to.
func (s *Server) backOnTarget(w *selfUpdateWait) bool {
	s.mu.RLock()
	_, connected := s.streams[w.hostID]
	s.mu.RUnlock()
	return connected && s.runsTarget(w, s.containersOf(w.hostID))
}

func (s *Server) runsTarget(w *selfUpdateWait, containers []cluster.ContainerInfo) bool {
	for _, c := range containers {
		if c.Name == w.name {
			return c.State == "running" && w.rec.NewImage != "" && c.Image == w.rec.NewImage
		}
	}
	return false
}

func (s *Server) containersOf(hostID string) []cluster.ContainerInfo {
	if hs, ok := s.registry.Get(hostID); ok {
		return hs.Containers
	}
	return nil
}

// selfUpdateReported finishes the self-updates of hostID waiting for an
// agent that came back without reporting outcomes (one older than v3.0.0,
// after a downgrade): done when it runs the new image. An agent that
// reports the outcome is waited for.
func (s *Server) selfUpdateReported(hostID string, containers []cluster.ContainerInfo, features []string) {
	if slices.Contains(features, cluster.FeatureSelfUpdateOutcome) {
		return
	}
	var done []*selfUpdateWait
	s.selfMu.Lock()
	for key, w := range s.selfWaits {
		if w.hostID == hostID && s.runsTarget(w, containers) {
			delete(s.selfWaits, key)
			w.timer.Stop()
			done = append(done, w)
		}
	}
	s.selfMu.Unlock()
	for _, w := range done {
		s.finishSelfUpdate(w, "success", "")
	}
}

// finishSelfUpdate records w with its outcome and, for a caller that
// notifies, notifies it. A later report of the same request (an agent
// that comes back after the wait ended) replaces the row.
func (s *Server) finishSelfUpdate(w *selfUpdateWait, outcome, errText string) {
	rec := w.rec
	rec.Outcome, rec.Error = outcome, errText
	rec.Duration = time.Since(w.started)
	if outcome != "success" {
		rec.NewDigest = ""
	}
	if s.history != nil {
		if err := s.history.RecordUpdate(rec); err != nil {
			s.log.Warn("failed to record an agent self-update", "hostID", w.hostID, "container", w.name, "error", err)
		}
	}
	s.rememberLate(w.hostID, w.requestID, "self-update", rec.Timestamp)
	s.log.Info("agent self-update finished", "hostID", w.hostID, "container", w.name, "outcome", outcome, "error", errText)
	// The parked old container and the helper are gone, or nearly: the
	// dashboard and the queue need not wait for the next scan to drop them.
	go s.refreshAfterSelfUpdate(w.hostID)
	msg := fmt.Sprintf("update %s: %s", w.name, outcome)
	if errText != "" {
		msg = fmt.Sprintf("update %s failed: %s", w.name, errText)
	}
	s.bus.Publish(events.SSEEvent{
		Type: events.EventContainerUpdate, ContainerName: w.name, HostID: w.hostID, HostName: rec.HostName,
		Message: msg, Timestamp: time.Now(),
	})
	if !w.notify {
		return
	}
	if fn := s.selfNotify.Load(); fn != nil {
		(*fn)(s.hostLabel(w.hostID), w.name, rec)
	}
}

// selfUpdateRefreshAgain is when the hub asks again for the containers of a
// host whose agent's update of itself is over: an agent that does not
// report outcomes is taken as updated while its helper may still be
// finishing (it settles for 20 seconds).
var selfUpdateRefreshAgain = 30 * time.Second

// SetHostRefreshed sets what is told a host's containers when the hub has
// asked for them anew, once an agent's update of itself is over (the
// queue drops what left the host).
func (s *Server) SetHostRefreshed(fn func(hostID string, containers []cluster.ContainerInfo)) {
	if fn == nil {
		s.hostRefreshed.Store(nil)
		return
	}
	s.hostRefreshed.Store(&fn)
}

// refreshAfterSelfUpdate asks the agent on hostID for its containers, now
// and again a little later, and tells the dashboard and SetHostRefreshed.
func (s *Server) refreshAfterSelfUpdate(hostID string) {
	s.refreshContainers(hostID)
	time.AfterFunc(selfUpdateRefreshAgain, func() { s.refreshContainers(hostID) })
}

func (s *Server) refreshContainers(hostID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	containers, err := s.ListContainersSync(ctx, hostID) // updates what the hub shows
	if err != nil {
		s.log.Debug("could not refresh the host's containers after a self-update", "hostID", hostID, "error", err)
		return
	}
	// A state event with no container reloads the dashboard.
	s.bus.Publish(events.SSEEvent{Type: events.EventContainerState, HostID: hostID, Timestamp: time.Now()})
	if fn := s.hostRefreshed.Load(); fn != nil {
		(*fn)(hostID, containers)
	}
}

// stopSelfUpdateWaits stops waiting for outcomes (the hub is stopping).
func (s *Server) stopSelfUpdateWaits() {
	s.selfMu.Lock()
	defer s.selfMu.Unlock()
	for key, w := range s.selfWaits {
		w.timer.Stop()
		delete(s.selfWaits, key)
	}
}

// hostName is hostID's host name, or "".
func (s *Server) hostName(hostID string) string {
	if hs, ok := s.registry.Get(hostID); ok {
		return hs.Info.Name
	}
	return ""
}

// selfUpdateJournalled takes a journal entry about an agent's update of
// itself: the result "under way" of one whose answer the stream lost (the
// hub then waits for its outcome), or the outcome of one the hub waits
// for, which it records and notifies. It reports whether e was one.
func (s *Server) selfUpdateJournalled(hostID string, e *proto.JournalEntry) bool {
	if e.GetOutcome() == cluster.OutcomePending {
		// The caller got no answer, and recorded (and notified) a failure
		// at recordAt; the outcome replaces that row.
		s.awaitSelfUpdate(hostID, &proto.UpdateResult{
			RequestId: e.GetId(), ContainerName: e.GetContainer(), OldImage: e.GetOldImage(), OldDigest: e.GetOldDigest(),
			NewImage: e.GetNewImage(), NewDigest: e.GetNewDigest(), Duration: e.GetDuration(),
		}, s.takeLate(hostID, e.GetId()).recordAt, false)
		return true
	}
	w := s.takeSelfUpdate(hostID, e.GetId())
	if w == nil {
		return false
	}
	if e.GetNewDigest() != "" {
		w.rec.NewDigest = e.GetNewDigest()
	}
	s.finishSelfUpdate(w, e.GetOutcome(), e.GetError())
	return true
}

// selfUpdateAnswered takes the "under way" answer to an update the agent's
// helper does (OutcomePending). The hub starts waiting for the outcome
// before the caller is answered, so an outcome the agent reports straight
// after cannot be missed. Answered after the caller gave up (it recorded,
// and notified, a failure), the outcome replaces the caller's row.
func (s *Server) selfUpdateAnswered(hostID string, msg *proto.AgentMessage, ur *proto.UpdateResult) {
	if waiting, records := s.pendingCaller(hostID, ur.RequestId); waiting {
		s.awaitSelfUpdate(hostID, ur, time.Time{}, records)
		if s.deliverPending(hostID, ur.RequestId, msg) {
			return
		}
		s.takeSelfUpdate(hostID, ur.RequestId) // the caller gave up meanwhile
	}
	s.awaitSelfUpdate(hostID, ur, s.takeLate(hostID, ur.RequestId).recordAt, false)
}
