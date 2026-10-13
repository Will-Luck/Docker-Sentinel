package server

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/cluster/proto"
)

// blackholeProxy forwards TCP between a client and target until drop is
// set, then silently discards everything, as a network partition does: the
// connection stays open but nothing gets through.
type blackholeProxy struct {
	lis  net.Listener
	drop atomic.Bool
}

func newBlackholeProxy(t *testing.T, target string) *blackholeProxy {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &blackholeProxy{lis: lis}
	t.Cleanup(func() { lis.Close() })
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			s, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			go p.pipe(c, s)
			go p.pipe(s, c)
		}
	}()
	return p
}

func (p *blackholeProxy) pipe(dst, src net.Conn) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && !p.drop.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// After a network partition the hub must notice the dead agent link on
// its own (keepalive pings), not wait for TCP to give up minutes later.
func TestKeepalive_HubNoticesPartitionedAgent(t *testing.T) {
	defer func(tm, to time.Duration) { keepaliveTime, keepaliveTimeout = tm, to }(keepaliveTime, keepaliveTimeout)
	keepaliveTime, keepaliveTimeout = time.Second, time.Second

	srv, addr, _, _ := testServer(t)
	token, _, _ := srv.GenerateEnrollToken(5 * time.Minute)
	hostID, certPEM, keyPEM, caPEM := enrollAgent(t, addr, token)

	proxy := newBlackholeProxy(t, addr)
	conn := agentTLSConn(t, proxy.lis.Addr().String(), certPEM, keyPEM, caPEM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := proto.NewAgentServiceClient(conn).Channel(ctx); err != nil {
		t.Fatalf("Channel: %v", err)
	}
	waitFor(t, 3*time.Second, func() bool {
		hs, _ := srv.Registry().Get(hostID)
		return hs.Connected
	}, "agent should connect through the proxy")

	proxy.drop.Store(true)

	waitFor(t, 6*time.Second, func() bool {
		hs, _ := srv.Registry().Get(hostID)
		return !hs.Connected
	}, "hub still shows the agent connected 6s into a partition")
}
