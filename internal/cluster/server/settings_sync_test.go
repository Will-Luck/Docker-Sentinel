package server

import (
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// The hub sends its agents the settings they follow when they connect, and
// again when one of them has changed (checked at each heartbeat), never
// for nothing.
func TestChannel_SendsSettingsOnConnectAndOnChange(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	var mu sync.Mutex
	current := &proto.SettingsSync{ImageCleanup: true, RemoveVolumes: false, HooksEnabled: true}
	srv.SetSettingsSource(func() *proto.SettingsSync {
		mu.Lock()
		defer mu.Unlock()
		return current
	})
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	t.Cleanup(closeConn)

	got := make(chan *proto.SettingsSync, 8)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if ss := msg.GetSettingsSync(); ss != nil {
				got <- ss
			}
		}
	}()
	next := func(what string) *proto.SettingsSync {
		t.Helper()
		select {
		case ss := <-got:
			return ss
		case <-time.After(30 * time.Second):
			t.Fatalf("no settings sent %s", what)
			return nil
		}
	}
	heartbeat := func() {
		t.Helper()
		if err := stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_Heartbeat{Heartbeat: &proto.Heartbeat{Timestamp: timestamppb.Now()}}}); err != nil {
			t.Fatal(err)
		}
	}

	if ss := next("on connect"); !ss.GetImageCleanup() || ss.GetRemoveVolumes() || !ss.GetHooksEnabled() {
		t.Errorf("settings on connect = %v, want the hub's", ss)
	}

	heartbeat()
	select {
	case ss := <-got:
		t.Errorf("settings sent again although nothing changed: %v", ss)
	case <-time.After(300 * time.Millisecond):
	}

	mu.Lock()
	current = &proto.SettingsSync{ImageCleanup: true, RemoveVolumes: true, HooksEnabled: true}
	mu.Unlock()
	heartbeat()
	if ss := next("after a change"); !ss.GetRemoveVolumes() {
		t.Errorf("settings after the change = %v, want remove_volumes on", ss)
	}
}
