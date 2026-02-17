// Package agent — sync.go handles the offline action journal and reconnection
// sync. When the agent operates autonomously, all observed state changes are
// recorded in a file-backed journal. On reconnection, the journal is sent to
// the server for processing and then cleared.
//
// The journal is a simple JSON file (not BoltDB) since the agent doesn't run
// its own BoltDB instance — it's designed to be lightweight and self-contained.
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

const journalFilename = "offline_journal.json"

// journal manages the offline action journal backed by a JSON file.
// Entries are appended during autonomous operation and drained on
// reconnection. The file is the source of truth — the in-memory slice
// is just a cache for fast reads.
type journal struct {
	mu      sync.Mutex
	path    string
	entries []cluster.JournalEntry
}

// newJournal creates a journal backed by a file in dataDir.
// Loads any existing entries from disk (surviving agent restarts).
func newJournal(dataDir string) (*journal, error) {
	j := &journal{
		path: filepath.Join(dataDir, journalFilename),
	}
	if err := j.load(); err != nil {
		return nil, fmt.Errorf("load journal: %w", err)
	}
	return j, nil
}

// Add appends an entry to the journal and persists to disk.
func (j *journal) Add(entry cluster.JournalEntry) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	// Default timestamp if not set.
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}

	j.entries = append(j.entries, entry)
	return j.save()
}

// Entries returns a snapshot of all journal entries.
func (j *journal) Entries() []cluster.JournalEntry {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]cluster.JournalEntry, len(j.entries))
	copy(out, j.entries)
	return out
}

// Len returns the number of journal entries without copying.
func (j *journal) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.entries)
}

// Clear removes all entries from the journal and deletes the file.
// Called after a successful sync to the server.
func (j *journal) Clear() error {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.entries = nil

	// Remove the file entirely — a missing file is equivalent to an
	// empty journal on next load.
	if err := os.Remove(j.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove journal file: %w", err)
	}
	return nil
}

// load reads journal entries from disk. If the file doesn't exist,
// the journal starts empty (not an error — first run or already cleared).
func (j *journal) load() error {
	data, err := os.ReadFile(j.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read journal: %w", err)
	}

	// Empty file is fine — treat as no entries.
	if len(data) == 0 {
		return nil
	}

	if err := json.Unmarshal(data, &j.entries); err != nil {
		return fmt.Errorf("unmarshal journal: %w", err)
	}
	return nil
}

// save writes all entries to disk as JSON. Must be called with j.mu held.
func (j *journal) save() error {
	data, err := json.MarshalIndent(j.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal journal: %w", err)
	}

	if err := os.WriteFile(j.path, data, 0600); err != nil {
		return fmt.Errorf("write journal: %w", err)
	}
	return nil
}

// --- Reconnection sync ---

// syncJournal sends the offline journal to the server over the bidirectional
// stream and clears it. Called after a successful reconnection, before the
// normal heartbeat/receive loops start.
//
// The sync is fire-and-forget for now — the server stubs journal processing
// and will ACK implicitly. If the send fails, the journal stays on disk for
// the next reconnection attempt.
func (a *Agent) syncJournal(stream proto.AgentService_ChannelClient) error {
	entries := a.journal.Entries()
	if len(entries) == 0 {
		return nil
	}

	a.log.Info("syncing offline journal to server", "entries", len(entries))

	// Convert cluster.JournalEntry -> proto.JournalEntry.
	pbEntries := make([]*proto.JournalEntry, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		pbEntries = append(pbEntries, &proto.JournalEntry{
			Id:        e.ID,
			Timestamp: timestamppb.New(e.Timestamp),
			Action:    e.Action,
			Container: e.Container,
			OldImage:  e.OldImage,
			NewImage:  e.NewImage,
			OldDigest: e.OldDigest,
			NewDigest: e.NewDigest,
			Outcome:   e.Outcome,
			Error:     e.Error,
			Duration:  durationpb.New(e.Duration),
		})
	}

	msg := &proto.AgentMessage{
		Payload: &proto.AgentMessage_OfflineJournal{
			OfflineJournal: &proto.OfflineJournal{
				Entries: pbEntries,
			},
		},
	}

	if err := stream.Send(msg); err != nil {
		return fmt.Errorf("send offline journal: %w", err)
	}

	// Clear the journal now that it's been sent. If the server drops the
	// connection before processing, the entries are lost — but this is
	// acceptable for MVP since the journal is informational (health
	// monitoring only), not transactional.
	if err := a.journal.Clear(); err != nil {
		a.log.Error("failed to clear journal after sync", "error", err)
		// Non-fatal — the entries were sent successfully. Worst case
		// they get re-sent on next reconnect (server should dedup).
	}

	a.log.Info("offline journal synced and cleared", "entries", len(entries))
	return nil
}
