package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Logger is a minimal logging interface.
type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

// S3Uploader uploads backup files to S3-compatible storage.
type S3Uploader interface {
	Upload(ctx context.Context, localPath, objectName string) error
}

// Info describes a single backup file.
type Info struct {
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// Manager handles backup creation, listing, and cleanup.
type Manager struct {
	db       *bolt.DB
	dir      string
	log      Logger
	uploader S3Uploader // optional, nil if S3 not configured
	retain   int        // number of local backups to keep (0 = unlimited)
}

// NewManager creates a backup manager. The dir is created if it doesn't exist.
func NewManager(db *bolt.DB, dir string, log Logger) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create backup dir: %w", err)
	}
	return &Manager{
		db:     db,
		dir:    dir,
		log:    log,
		retain: 7, // default retention
	}, nil
}

// SetRetention configures how many local backups to keep.
func (m *Manager) SetRetention(n int) {
	if n < 0 {
		n = 0
	}
	m.retain = n
}

// SetUploader configures the optional S3 uploader.
func (m *Manager) SetUploader(u S3Uploader) {
	m.uploader = u
}

// CreateBackup creates a hot backup of the BoltDB database.
// Returns the Info of the created backup file.
func (m *Manager) CreateBackup(ctx context.Context) (*Info, error) {
	now := time.Now().UTC()
	filename := fmt.Sprintf("sentinel-%s-%d.db", now.Format("20060102-150405"), now.UnixMilli()%1000)
	path := filepath.Join(m.dir, filename)

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create backup file: %w", err)
	}

	// BoltDB's Tx.WriteTo produces a consistent snapshot.
	err = m.db.View(func(tx *bolt.Tx) error {
		_, wErr := tx.WriteTo(f)
		return wErr
	})
	closeErr := f.Close()
	if err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("write backup: %w", err)
	}
	if closeErr != nil {
		os.Remove(path)
		return nil, fmt.Errorf("close backup file: %w", closeErr)
	}

	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat backup: %w", err)
	}

	info := &Info{
		Filename:  filename,
		Size:      stat.Size(),
		CreatedAt: time.Now().UTC(),
	}

	m.log.Info("backup created", "filename", filename, "size", stat.Size())

	// Upload to S3 if configured.
	if m.uploader != nil {
		if upErr := m.uploader.Upload(ctx, path, filename); upErr != nil {
			m.log.Error("S3 upload failed", "filename", filename, "error", upErr)
			// Don't fail the backup itself; local copy is still valid.
		} else {
			m.log.Info("backup uploaded to S3", "filename", filename)
		}
	}

	// Enforce retention.
	if m.retain > 0 {
		m.enforceRetention()
	}

	return info, nil
}

// List returns all backup files sorted by creation time (newest first).
func (m *Manager) List() ([]Info, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, fmt.Errorf("read backup dir: %w", err)
	}

	var backups []Info
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".db" {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, Info{
			Filename:  e.Name(),
			Size:      fi.Size(),
			CreatedAt: fi.ModTime(),
		})
	}

	// Sort newest first.
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})

	return backups, nil
}

// FilePath returns the full path to a backup file, or error if it doesn't exist.
func (m *Manager) FilePath(filename string) (string, error) {
	// Sanitise: only allow the base name, no path traversal.
	clean := filepath.Base(filename)
	if clean != filename || clean == "." || clean == ".." {
		return "", fmt.Errorf("invalid filename")
	}
	path := filepath.Join(m.dir, clean)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("backup not found: %s", clean)
	}
	return path, nil
}

// Dir returns the backup directory path.
func (m *Manager) Dir() string {
	return m.dir
}
