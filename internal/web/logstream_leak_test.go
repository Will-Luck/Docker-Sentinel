package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func muxFrame(text string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = 1
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(text))) //nolint:gosec // test frame, the length is tiny
	return append(hdr, []byte(text)...)
}

func waitGoroutines(t *testing.T, base int) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if runtime.NumGoroutine() <= base {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("goroutines = %d, want at most %d: the log reader is still blocked after the client left", runtime.NumGoroutine(), base)
}

// When the client disconnects the reader goroutine must exit, not stay
// blocked sending a frame nobody will take.
func TestStreamMuxLogs_ReaderStopsWhenClientLeaves(t *testing.T) {
	base := runtime.NumGoroutine()
	var data bytes.Buffer
	for i := 0; i < 5; i++ {
		data.Write(muxFrame("line\n"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	srv := &Server{}
	srv.streamMuxLogs(w, w, ctx, &data)
	waitGoroutines(t, base)
}

func TestStreamTTYLogs_ReaderStopsWhenClientLeaves(t *testing.T) {
	base := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	srv := &Server{}
	srv.streamTTYLogs(w, w, ctx, io.Reader(strings.NewReader(strings.Repeat("line\n", 50))))
	waitGoroutines(t, base)
}
