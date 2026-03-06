package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock: ContainerLogViewer
// ---------------------------------------------------------------------------

type mockLogViewer struct {
	logs string
	err  error
}

func (m *mockLogViewer) ContainerLogs(_ context.Context, _ string, _ int) (string, error) {
	return m.logs, m.err
}

// ---------------------------------------------------------------------------
// Mock: ContainerLogStreamer
// ---------------------------------------------------------------------------

type mockLogStreamer struct {
	reader io.ReadCloser
	tty    bool
	err    error
}

func (m *mockLogStreamer) ContainerLogStream(_ context.Context, _ string, _ int) (io.ReadCloser, bool, error) {
	return m.reader, m.tty, m.err
}

// ---------------------------------------------------------------------------
// Mock: ContainerLister that returns errors
// ---------------------------------------------------------------------------

type errContainerLister struct {
	err error
}

func (m *errContainerLister) ListContainers(_ context.Context) ([]ContainerSummary, error) {
	return nil, m.err
}

func (m *errContainerLister) ListAllContainers(_ context.Context) ([]ContainerSummary, error) {
	return nil, m.err
}

func (m *errContainerLister) InspectContainer(_ context.Context, _ string) (ContainerInspect, error) {
	return ContainerInspect{}, m.err
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newLogsTestServer(docker ContainerLister, logViewer ContainerLogViewer, logStreamer ContainerLogStreamer, cluster *ClusterController) *Server {
	if cluster == nil {
		cluster = NewClusterController()
	}
	return &Server{
		deps: Dependencies{
			Docker:      docker,
			LogViewer:   logViewer,
			LogStreamer: logStreamer,
			Cluster:     cluster,
			Log:         discardLogger(),
		},
	}
}

func decodeLogResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, w.Body.String())
	}
	return m
}

// buildMuxFrame builds a Docker multiplexed log frame.
// streamType: 1 = stdout, 2 = stderr.
func buildMuxFrame(streamType byte, payload string) []byte {
	hdr := make([]byte, 8)
	hdr[0] = streamType
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(payload))) //nolint:gosec // test helper, payload is always small
	return append(hdr, []byte(payload)...)
}

// ---------------------------------------------------------------------------
// apiContainerLogs tests
// ---------------------------------------------------------------------------

func TestApiContainerLogs_EmptyName(t *testing.T) {
	srv := newLogsTestServer(nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers//logs", nil)
	// PathValue("name") returns "" when no name segment is present.
	srv.apiContainerLogs(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestApiContainerLogs_MissingLogViewer(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "running"},
		},
	}
	// LogViewer intentionally nil.
	srv := newLogsTestServer(docker, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogs(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}

func TestApiContainerLogs_ContainerNotFound(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{}, // empty list
	}
	viewer := &mockLogViewer{logs: "should not be called"}
	srv := newLogsTestServer(docker, viewer, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nonexistent/logs", nil)
	r.SetPathValue("name", "nonexistent")
	srv.apiContainerLogs(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestApiContainerLogs_Success(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "running"},
		},
	}
	viewer := &mockLogViewer{logs: "line1\nline2\nline3"}
	srv := newLogsTestServer(docker, viewer, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs?lines=100", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogs(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	result := decodeLogResponse(t, w)
	if result["logs"] != "line1\nline2\nline3" {
		t.Errorf("logs = %q, want %q", result["logs"], "line1\nline2\nline3")
	}
	if result["lines"] != float64(100) {
		t.Errorf("lines = %v, want 100", result["lines"])
	}
	if result["remote"] != false {
		t.Errorf("remote = %v, want false", result["remote"])
	}
}

func TestApiContainerLogs_LinesCapping(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "running"},
		},
	}
	viewer := &mockLogViewer{logs: "capped"}
	srv := newLogsTestServer(docker, viewer, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs?lines=9999", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogs(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	result := decodeLogResponse(t, w)
	// Lines should be capped to 500.
	if result["lines"] != float64(500) {
		t.Errorf("lines = %v, want 500 (capped from 9999)", result["lines"])
	}
}

func TestApiContainerLogs_DefaultLines(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "running"},
		},
	}
	viewer := &mockLogViewer{logs: "default"}
	srv := newLogsTestServer(docker, viewer, nil, nil)

	w := httptest.NewRecorder()
	// No lines parameter.
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogs(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	result := decodeLogResponse(t, w)
	// Default is 50.
	if result["lines"] != float64(50) {
		t.Errorf("lines = %v, want 50 (default)", result["lines"])
	}
}

func TestApiContainerLogs_RemoteClusterNil(t *testing.T) {
	docker := &mockContainerLister{}
	viewer := &mockLogViewer{logs: "local"}
	// Cluster has no provider set, so Enabled() returns false.
	srv := newLogsTestServer(docker, viewer, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs?host=h1", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogs(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}

func TestApiContainerLogs_ListContainersError(t *testing.T) {
	docker := &errContainerLister{err: errors.New("docker daemon unavailable")}
	viewer := &mockLogViewer{logs: "should not reach"}
	srv := newLogsTestServer(docker, viewer, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogs(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// apiContainerLogStream tests
// ---------------------------------------------------------------------------

func TestApiContainerLogStream_EmptyName(t *testing.T) {
	srv := newLogsTestServer(nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers//logs/stream", nil)
	srv.apiContainerLogStream(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestApiContainerLogStream_RemoteHost501(t *testing.T) {
	srv := newLogsTestServer(nil, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs/stream?host=h1", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogStream(w, r)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotImplemented, w.Body.String())
	}
}

func TestApiContainerLogStream_MissingLogStreamer(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "running"},
		},
	}
	// LogStreamer intentionally nil.
	srv := newLogsTestServer(docker, nil, nil, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs/stream", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogStream(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
}

func TestApiContainerLogStream_ContainerNotFound(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{}, // empty
	}
	streamer := &mockLogStreamer{}
	srv := newLogsTestServer(docker, nil, streamer, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/ghost/logs/stream", nil)
	r.SetPathValue("name", "ghost")
	srv.apiContainerLogStream(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusNotFound, w.Body.String())
	}
}

func TestApiContainerLogStream_TTY(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "running"},
		},
	}

	ttyData := "log line 1\nlog line 2\nlog line 3\n"
	streamer := &mockLogStreamer{
		reader: io.NopCloser(bytes.NewReader([]byte(ttyData))),
		tty:    true,
	}
	srv := newLogsTestServer(docker, nil, streamer, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs/stream", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogStream(w, r)

	body := w.Body.String()

	// Check SSE headers.
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/event-stream")
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-cache")
	}
	if conn := w.Header().Get("Connection"); conn != "keep-alive" {
		t.Errorf("Connection = %q, want %q", conn, "keep-alive")
	}
	if xab := w.Header().Get("X-Accel-Buffering"); xab != "no" {
		t.Errorf("X-Accel-Buffering = %q, want %q", xab, "no")
	}

	// Check "connected" event.
	if !strings.Contains(body, "event: connected\ndata: {}\n\n") {
		t.Errorf("missing connected event in body:\n%s", body)
	}

	// Check log lines are streamed as SSE data frames.
	if !strings.Contains(body, "data: log line 1\n\n") {
		t.Errorf("missing 'data: log line 1' in body:\n%s", body)
	}
	if !strings.Contains(body, "data: log line 2\n\n") {
		t.Errorf("missing 'data: log line 2' in body:\n%s", body)
	}
	if !strings.Contains(body, "data: log line 3\n\n") {
		t.Errorf("missing 'data: log line 3' in body:\n%s", body)
	}

	// Check "eof" event at the end.
	if !strings.Contains(body, "event: eof\ndata: {}\n\n") {
		t.Errorf("missing eof event in body:\n%s", body)
	}
}

func TestApiContainerLogStream_MuxFormat(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "def456", Names: []string{"/redis"}, State: "running"},
		},
	}

	// Build multiplexed Docker log data: two stdout frames.
	var buf bytes.Buffer
	buf.Write(buildMuxFrame(1, "stdout line 1\n"))
	buf.Write(buildMuxFrame(2, "stderr line 1\n"))

	streamer := &mockLogStreamer{
		reader: io.NopCloser(bytes.NewReader(buf.Bytes())),
		tty:    false,
	}
	srv := newLogsTestServer(docker, nil, streamer, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/redis/logs/stream", nil)
	r.SetPathValue("name", "redis")
	srv.apiContainerLogStream(w, r)

	body := w.Body.String()

	// Check SSE headers.
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/event-stream")
	}

	// Check "connected" event.
	if !strings.Contains(body, "event: connected\ndata: {}\n\n") {
		t.Errorf("missing connected event in body:\n%s", body)
	}

	// Check both stdout and stderr lines appear as SSE data frames.
	if !strings.Contains(body, "data: stdout line 1\n\n") {
		t.Errorf("missing 'data: stdout line 1' in body:\n%s", body)
	}
	if !strings.Contains(body, "data: stderr line 1\n\n") {
		t.Errorf("missing 'data: stderr line 1' in body:\n%s", body)
	}

	// Check "eof" event.
	if !strings.Contains(body, "event: eof\ndata: {}\n\n") {
		t.Errorf("missing eof event in body:\n%s", body)
	}
}

func TestApiContainerLogStream_StreamError(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}, State: "running"},
		},
	}
	streamer := &mockLogStreamer{
		err: errors.New("stream init failed"),
	}
	srv := newLogsTestServer(docker, nil, streamer, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs/stream", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogStream(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

func TestApiContainerLogStream_ListContainersError(t *testing.T) {
	docker := &errContainerLister{err: errors.New("cannot list")}
	streamer := &mockLogStreamer{}
	srv := newLogsTestServer(docker, nil, streamer, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/nginx/logs/stream", nil)
	r.SetPathValue("name", "nginx")
	srv.apiContainerLogStream(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// resolveContainerID tests
// ---------------------------------------------------------------------------

func TestResolveContainerID_StripsLeadingSlash(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}},
			{ID: "def456", Names: []string{"/redis"}},
		},
	}
	srv := newLogsTestServer(docker, nil, nil, nil)

	id, err := srv.resolveContainerID(context.Background(), "nginx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "abc123" {
		t.Errorf("id = %q, want %q", id, "abc123")
	}
}

func TestResolveContainerID_UnknownContainer(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/nginx"}},
		},
	}
	srv := newLogsTestServer(docker, nil, nil, nil)

	id, err := srv.resolveContainerID(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty string for unknown container", id)
	}
}

func TestResolveContainerID_ListError(t *testing.T) {
	docker := &errContainerLister{err: errors.New("daemon down")}
	srv := newLogsTestServer(docker, nil, nil, nil)

	_, err := srv.resolveContainerID(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error from ListAllContainers, got nil")
	}
}

func TestResolveContainerID_NameWithoutSlash(t *testing.T) {
	// Some container names may not have a leading slash.
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "xyz789", Names: []string{"bare-name"}},
		},
	}
	srv := newLogsTestServer(docker, nil, nil, nil)

	id, err := srv.resolveContainerID(context.Background(), "bare-name")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "xyz789" {
		t.Errorf("id = %q, want %q", id, "xyz789")
	}
}

func TestResolveContainerID_MultipleNames(t *testing.T) {
	// A container can have multiple names; match any of them.
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "multi1", Names: []string{"/alias1", "/alias2"}},
		},
	}
	srv := newLogsTestServer(docker, nil, nil, nil)

	id, err := srv.resolveContainerID(context.Background(), "alias2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "multi1" {
		t.Errorf("id = %q, want %q", id, "multi1")
	}
}

// ---------------------------------------------------------------------------
// Mux frame edge cases
// ---------------------------------------------------------------------------

func TestApiContainerLogStream_MuxMultilinePayload(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/app"}, State: "running"},
		},
	}

	// Single mux frame containing multiple lines.
	var buf bytes.Buffer
	buf.Write(buildMuxFrame(1, "first\nsecond\nthird\n"))

	streamer := &mockLogStreamer{
		reader: io.NopCloser(bytes.NewReader(buf.Bytes())),
		tty:    false,
	}
	srv := newLogsTestServer(docker, nil, streamer, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/app/logs/stream", nil)
	r.SetPathValue("name", "app")
	srv.apiContainerLogStream(w, r)

	body := w.Body.String()

	// Each line within the payload should be a separate SSE data frame.
	if !strings.Contains(body, "data: first\n\n") {
		t.Errorf("missing 'data: first' in body:\n%s", body)
	}
	if !strings.Contains(body, "data: second\n\n") {
		t.Errorf("missing 'data: second' in body:\n%s", body)
	}
	if !strings.Contains(body, "data: third\n\n") {
		t.Errorf("missing 'data: third' in body:\n%s", body)
	}
}

func TestApiContainerLogStream_MuxZeroLengthFrame(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/app"}, State: "running"},
		},
	}

	// A zero-length mux frame followed by a normal frame.
	var buf bytes.Buffer
	zeroHdr := make([]byte, 8)
	zeroHdr[0] = 1
	// bytes 4-7 stay 0 (zero payload length)
	buf.Write(zeroHdr)
	buf.Write(buildMuxFrame(1, "after-zero\n"))

	streamer := &mockLogStreamer{
		reader: io.NopCloser(bytes.NewReader(buf.Bytes())),
		tty:    false,
	}
	srv := newLogsTestServer(docker, nil, streamer, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/app/logs/stream", nil)
	r.SetPathValue("name", "app")
	srv.apiContainerLogStream(w, r)

	body := w.Body.String()

	// The zero-length frame should be skipped; the normal frame appears.
	if !strings.Contains(body, "data: after-zero\n\n") {
		t.Errorf("missing 'data: after-zero' in body:\n%s", body)
	}
}

func TestApiContainerLogStream_SSEInjectionEscaped(t *testing.T) {
	docker := &mockContainerLister{
		containers: []ContainerSummary{
			{ID: "abc123", Names: []string{"/app"}, State: "running"},
		},
	}

	// Malicious log line that tries to inject a fake SSE event.
	// In TTY mode, bufio.Scanner splits on \n so each piece becomes its own
	// data: frame. The sseEscapeLine call is defense-in-depth. We verify that
	// no actual SSE event injection occurs (no un-prefixed "event:" lines
	// besides the legitimate connected/eof).
	malicious := "normal log\n\nevent: custom\ndata: injected"
	ttyData := malicious + "\n"
	streamer := &mockLogStreamer{
		reader: io.NopCloser(bytes.NewReader([]byte(ttyData))),
		tty:    true,
	}
	srv := newLogsTestServer(docker, nil, streamer, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/containers/app/logs/stream", nil)
	r.SetPathValue("name", "app")
	srv.apiContainerLogStream(w, r)

	body := w.Body.String()

	// Every line of the malicious content must be wrapped in a "data: " frame.
	// Check that no line starts with "event:" except the legitimate ones.
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "event: ") && line != "event: connected" && line != "event: eof" {
			t.Errorf("SSE injection: unexpected event line %q in body:\n%s", line, body)
		}
	}

	// All malicious content must appear inside data: frames (prefixed).
	if !strings.Contains(body, "data: event: custom") {
		t.Errorf("expected malicious content wrapped in data frame, body:\n%s", body)
	}
}

func TestSseEscapeLine(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"normal line", "normal line"},
		{"line\nwith newline", "line\\nwith newline"},
		{"line\rwith cr", "line\\rwith cr"},
		{"multi\n\nblank", "multi\\n\\nblank"},
		{"mixed\r\ncrlf", "mixed\\r\\ncrlf"},
	}
	for _, tt := range tests {
		got := sseEscapeLine(tt.input)
		if got != tt.want {
			t.Errorf("sseEscapeLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
