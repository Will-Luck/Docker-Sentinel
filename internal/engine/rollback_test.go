package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GiteaLN/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/container"
)

func TestRollbackCreatesContainerFromSnapshot(t *testing.T) {
	mock := newMockDocker()
	log := logging.New(false)

	snapshot := container.InspectResponse{
		ID:   "old-id",
		Name: "/nginx",
		Config: &container.Config{
			Image:  "docker.io/library/nginx:1.25",
			Labels: map[string]string{"sentinel.policy": "auto"},
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	err = rollback(context.Background(), mock, "nginx", data, log)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if len(mock.createCalls) != 1 || mock.createCalls[0] != "nginx" {
		t.Errorf("createCalls = %v, want [nginx]", mock.createCalls)
	}
	if len(mock.startCalls) != 1 {
		t.Errorf("startCalls = %d, want 1", len(mock.startCalls))
	}
}

func TestRollbackInvalidSnapshot(t *testing.T) {
	mock := newMockDocker()
	log := logging.New(false)

	err := rollback(context.Background(), mock, "nginx", []byte("not json"), log)
	if err == nil {
		t.Fatal("expected error for invalid snapshot data")
	}
}

func TestRollbackFromStore(t *testing.T) {
	mock := newMockDocker()
	log := logging.New(false)
	s := testStore(t)

	snapshot := container.InspectResponse{
		ID:   "old-id",
		Name: "/redis",
		Config: &container.Config{
			Image: "docker.io/library/redis:7",
		},
		HostConfig:      &container.HostConfig{},
		NetworkSettings: &container.NetworkSettings{},
	}
	data, _ := json.Marshal(snapshot)
	if err := s.SaveSnapshot("redis", data); err != nil {
		t.Fatal(err)
	}

	err := RollbackFromStore(context.Background(), mock, s, "redis", log)
	if err != nil {
		t.Fatalf("RollbackFromStore: %v", err)
	}

	if len(mock.createCalls) != 1 {
		t.Errorf("createCalls = %d, want 1", len(mock.createCalls))
	}
}

func TestRollbackFromStoreNoSnapshot(t *testing.T) {
	mock := newMockDocker()
	log := logging.New(false)
	s := testStore(t)

	err := RollbackFromStore(context.Background(), mock, s, "nonexistent", log)
	if err == nil {
		t.Fatal("expected error for missing snapshot")
	}
}
