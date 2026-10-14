package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster"
	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
)

// bigContainerList is n containers whose labels add up to about n*kb KB.
func bigContainerList(n, kb int) []*proto.ContainerInfo {
	label := strings.Repeat("x", kb<<10)
	out := make([]*proto.ContainerInfo, n)
	for i := range out {
		out[i] = &proto.ContainerInfo{
			Id: fmt.Sprintf("id%d", i), Name: fmt.Sprintf("app%d", i), Image: "nginx:1", State: "running",
			Labels: map[string]string{"com.example.big": label},
		}
	}
	return out
}

// A host with many containers and large labels sends a list over the
// 4 MB gRPC default. The hub must accept it, or the agent is locked out:
// its state report is rejected on every reconnect.
func TestLargeContainerList_Accepted(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	containers := bigContainerList(60, 100) // about 6 MB

	client := proto.NewAgentServiceClient(agentTLSConn(t, addr, certPEM, keyPEM, caPEM))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.ReportState(ctx, &proto.StateReport{HostId: hostID, Containers: containers}); err != nil {
		t.Fatalf("ReportState with a 6 MB container list: %v", err)
	}

	stream, _, closeConn := connectedStream(t, srv, addr, hostID, certPEM, keyPEM, caPEM, nil)
	defer closeConn()
	srv.Registry().UpdateContainers(hostID, nil)
	if err := stream.Send(&proto.AgentMessage{Payload: &proto.AgentMessage_ContainerList{
		ContainerList: &proto.ContainerList{Containers: containers},
	}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitFor(t, 30*time.Second, func() bool {
		hs, _ := srv.Registry().Get(hostID)
		return len(hs.Containers) == 60
	}, "6 MB container list on the stream was not accepted")
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Past the limit the message is still refused, and the hub log says which
// host sent it (gRPC on its own only tells the agent).
func TestOversizeMessage_LoggedWithHost(t *testing.T) {
	defer func(n int) { maxMessageSize = n }(maxMessageSize)
	maxMessageSize = 1 << 20

	ca, err := cluster.EnsureCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir() + "/hub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logs := &syncBuffer{}
	srv, err := New(ca, st, events.New(), slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close()
	if err := srv.Start(addr); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)

	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)
	client := proto.NewAgentServiceClient(agentTLSConn(t, addr, certPEM, keyPEM, caPEM))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := client.ReportState(ctx, &proto.StateReport{HostId: hostID, Containers: bigContainerList(20, 100)}); err == nil {
		t.Fatal("a 2 MB report was accepted over a 1 MB limit")
	}
	waitFor(t, 30*time.Second, func() bool {
		out := logs.String()
		return strings.Contains(out, "larger than") && strings.Contains(out, hostID)
	}, "hub log does not name the host whose message was too large")
}

// The 64 MB message limit applies before a client has shown a certificate,
// so one connection must not be able to open an unbounded number of
// streams. The server announces its limit in its HTTP/2 settings, which
// this reads by hand: a 9-byte frame header, then 6-byte (id, value) pairs.
func TestServer_LimitsConcurrentStreams(t *testing.T) {
	srv, addr, _, _ := testServer(t)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(srv.ca.CACertPEM())
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Client preface, then an empty SETTINGS frame.
	if _, err := conn.Write(append([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"), 0, 0, 0, 0x4, 0, 0, 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		hdr := make([]byte, 9)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			t.Fatalf("reading server frame: %v", err)
		}
		length := int(hdr[0])<<16 | int(hdr[1])<<8 | int(hdr[2])
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			t.Fatalf("reading server frame: %v", err)
		}
		const settingsFrame, ackFlag = 0x4, 0x1
		if hdr[3] != settingsFrame || hdr[4]&ackFlag != 0 {
			continue
		}
		const maxConcurrentStreams = 0x3
		for i := 0; i+6 <= len(payload); i += 6 {
			if binary.BigEndian.Uint16(payload[i:]) == maxConcurrentStreams {
				if v := binary.BigEndian.Uint32(payload[i+2:]); v != 64 {
					t.Errorf("server MAX_CONCURRENT_STREAMS = %d, want 64", v)
				}
				return
			}
		}
		t.Fatal("server announced no MAX_CONCURRENT_STREAMS limit")
	}
}
