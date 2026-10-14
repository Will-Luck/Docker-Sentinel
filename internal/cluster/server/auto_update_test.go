package server

import (
	"context"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

func TestBaseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"v2.0.1 (abc1234)", "v2.0.1"},
		{"v2.0.1", "v2.0.1"},
		{"dev (abc1234)", "dev"},
		{"dev", "dev"},
		{"", ""},
		{"  v2.0.1  ", "v2.0.1"},
		{"v2.0.1 (abc1234) extra", "v2.0.1"},
	}

	for _, tt := range tests {
		got := baseVersion(tt.input)
		if got != tt.want {
			t.Errorf("baseVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReplaceImageTag(t *testing.T) {
	tests := []struct {
		image  string
		newTag string
		want   string
	}{
		{"ghcr.io/foo/sentinel:v2.0.0", "v2.0.1", "ghcr.io/foo/sentinel:v2.0.1"},
		{"sentinel:latest", "v2.0.1", "sentinel:v2.0.1"},
		{"sentinel", "v2.0.1", "sentinel:v2.0.1"},
		{"registry.example.com:5000/sentinel:old", "v2.0.1", "registry.example.com:5000/sentinel:v2.0.1"},
		{"ghcr.io/will-luck/docker-sentinel:v2.0.0", "v2.1.0", "ghcr.io/will-luck/docker-sentinel:v2.1.0"},
		// Digest reference: strip digest and apply tag.
		{"ghcr.io/foo/sentinel@sha256:abc123", "v2.0.1", "ghcr.io/foo/sentinel:v2.0.1"},
		// Port-only registry without tag: colon is part of the hostname, not a tag.
		{"registry.example.com:5000/sentinel", "v2.0.1", "registry.example.com:5000/sentinel:v2.0.1"},
	}

	for _, tt := range tests {
		got := replaceImageTag(tt.image, tt.newTag)
		if got != tt.want {
			t.Errorf("replaceImageTag(%q, %q) = %q, want %q", tt.image, tt.newTag, got, tt.want)
		}
	}
}

// Release builds report a version such as "v2.16.1 (abc1234)", but the
// release workflow publishes the image as "2.16.1" with no "v". The target
// the hub sends to an out-of-date agent must name a tag that exists.
func TestCheckAgentVersions_TargetsPublishedImageTag(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	conn := agentTLSConn(t, addr, certPEM, keyPEM, caPEM)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := proto.NewAgentServiceClient(conn).Channel(ctx)
	if err != nil {
		t.Fatalf("Channel: %v", err)
	}

	const agentImage = "ghcr.io/will-luck/docker-sentinel:2.16.0"
	if err := stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_Heartbeat{
		Heartbeat: &proto.Heartbeat{AgentVersion: "v2.16.0 (abc1234)", HostId: hostID},
	}}); err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}
	if err := stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_ContainerList{
		ContainerList: &proto.ContainerList{Containers: []*proto.ContainerInfo{{
			Name:   "sentinel-agent",
			Image:  agentImage,
			State:  "running",
			Labels: map[string]string{"sentinel.self": "true"},
		}}},
	}}); err != nil {
		t.Fatalf("send container list: %v", err)
	}
	waitFor(t, 30*time.Second, func() bool {
		hs, ok := srv.Registry().Get(hostID)
		return ok && len(hs.Containers) == 1 && srv.AgentVersions()[hostID] != ""
	}, "server should have the agent's version and container list")

	go srv.CheckAgentVersions(ctx, "v2.16.1 (def5678)")

	var target string
	for target == "" {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("waiting for update request: %v", err)
		}
		req := msg.GetUpdateContainer()
		if req == nil {
			continue
		}
		target = req.GetTargetImage()
		_ = stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_UpdateResult{
			UpdateResult: &proto.UpdateResult{RequestId: msg.GetRequestId(), ContainerName: req.GetContainerName(), Outcome: "success"},
		}})
	}

	if want := "ghcr.io/will-luck/docker-sentinel:2.16.1"; target != want {
		t.Fatalf("auto-update target = %q, want the published tag %q", target, want)
	}
}

// An agent that already runs the hub's release must not be updated, even
// though one side reports "v2.16.1" and the image tag says "2.16.1".
func TestAgentNeedsUpdate_IgnoresVPrefix(t *testing.T) {
	tests := []struct {
		agent, server string
		want          bool
	}{
		{"v2.16.0 (abc)", "v2.16.1 (def)", true},
		{"v2.16.1 (abc)", "v2.16.1 (def)", false},
		{"2.16.1", "v2.16.1", false},
		{"dev", "v2.16.1", false},
		{"", "v2.16.1", false},
		{"v2.16.0", "dev", false},
	}
	for _, tt := range tests {
		if got := agentNeedsUpdate(tt.agent, tt.server); got != tt.want {
			t.Errorf("agentNeedsUpdate(%q, %q) = %v, want %v", tt.agent, tt.server, got, tt.want)
		}
	}
}
