// Quick tool to inject test entries into the Sentinel queue (BoltDB).
// Usage: go run ./cmd/inject-queue -db /path/to/sentinel.db
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	bolt "go.etcd.io/bbolt"
)

type PendingUpdate struct {
	ContainerID   string    `json:"container_id"`
	ContainerName string    `json:"container_name"`
	CurrentImage  string    `json:"current_image"`
	CurrentDigest string    `json:"current_digest"`
	RemoteDigest  string    `json:"remote_digest"`
	DetectedAt    time.Time `json:"detected_at"`
	NewerVersions []string  `json:"newer_versions,omitempty"`
}

func main() {
	dbPath := flag.String("db", "/var/lib/docker/volumes/sentinel-data/_data/sentinel.db", "path to sentinel.db")
	flag.Parse()

	entries := []PendingUpdate{
		{ContainerName: "test-alpha-util", CurrentImage: "alpine:3.23.2", NewerVersions: []string{"3.23.3"}, DetectedAt: time.Now()},
		{ContainerName: "test-alpha-cache", CurrentImage: "redis:7.0", NewerVersions: []string{"8.6"}, DetectedAt: time.Now().Add(-5 * time.Minute)},
		{ContainerName: "test-alpha-web", CurrentImage: "nginx:1.24", NewerVersions: []string{"1.29.5"}, DetectedAt: time.Now().Add(-10 * time.Minute)},
		{ContainerName: "test-beta-proxy", CurrentImage: "httpd:2.4.58", NewerVersions: []string{"2.4.63"}, DetectedAt: time.Now().Add(-15 * time.Minute)},
		{ContainerName: "test-beta-store", CurrentImage: "busybox:1.36", NewerVersions: []string{"1.37"}, DetectedAt: time.Now().Add(-20 * time.Minute)},
	}

	db, err := bolt.Open(*dbPath, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("queue"))
		if err != nil {
			return err
		}
		// Sentinel stores the queue as a single JSON array under key "pending".
		data, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		if err := b.Put([]byte("pending"), data); err != nil {
			return err
		}
		for _, e := range entries {
			fmt.Printf("  queued: %s (%s → %s)\n", e.ContainerName, e.CurrentImage, e.NewerVersions[0])
		}
		return nil
	})
	if err != nil {
		log.Fatalf("write queue: %v", err)
	}
	fmt.Printf("\nInjected %d entries. Restart Sentinel to pick them up.\n", len(entries))
}
