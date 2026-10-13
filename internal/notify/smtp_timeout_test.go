package notify

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestSMTPSilentServerHonoursDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // hold the connection open and never send a greeting
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	s := NewSMTP("127.0.0.1", port, "a@b", "c@d", "", "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Send(ctx, Event{Type: EventUpdateFailed, ContainerName: "x"}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from a silent server")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SMTP Send still blocked 2.5s after its 500ms context deadline")
	}
}
