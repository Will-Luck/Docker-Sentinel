package backup

import (
	"os"
	"path/filepath"
	"sort"
)

// enforceRetention deletes the oldest local backups beyond the retention limit.
func (m *Manager) enforceRetention() {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		m.log.Error("retention: failed to list backups", "error", err)
		return
	}

	// Collect .db files with their mod times.
	type fileEntry struct {
		name    string
		modTime int64 // unix nano for sorting
	}
	var files []fileEntry
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".db" {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{name: e.Name(), modTime: fi.ModTime().UnixNano()})
	}

	if len(files) <= m.retain {
		return
	}

	// Sort oldest first.
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime < files[j].modTime
	})

	// Delete the oldest files beyond the retention limit.
	toDelete := len(files) - m.retain
	for i := 0; i < toDelete; i++ {
		path := filepath.Join(m.dir, files[i].name)
		if err := os.Remove(path); err != nil {
			m.log.Error("retention: failed to delete", "file", files[i].name, "error", err)
		} else {
			m.log.Info("retention: deleted old backup", "file", files[i].name)
		}
	}
}
