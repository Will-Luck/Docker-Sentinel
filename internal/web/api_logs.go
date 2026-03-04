package web

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// apiContainerLogs returns the last N lines of a container's logs.
func (s *Server) apiContainerLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	// Parse lines early so it's available for both local and remote paths.
	lines := 50
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	if lines > 500 {
		lines = 500
	}

	// Remote containers — fetch logs via cluster gRPC.
	if host := r.URL.Query().Get("host"); host != "" {
		if s.deps.Cluster == nil || !s.deps.Cluster.Enabled() {
			writeError(w, http.StatusServiceUnavailable, "cluster not available")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		output, err := s.deps.Cluster.RemoteContainerLogs(ctx, host, name, lines)
		if err != nil {
			s.deps.Log.Error("remote logs failed", "name", name, "host", host, "error", err)
			writeError(w, http.StatusBadGateway, "failed to fetch remote logs: "+err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"logs":   output,
			"lines":  lines,
			"remote": true,
		})
		return
	}

	if s.deps.LogViewer == nil {
		writeError(w, http.StatusServiceUnavailable, "log viewer not available")
		return
	}

	containerID, err := s.resolveContainerID(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}
	if containerID == "" {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}

	output, err := s.deps.LogViewer.ContainerLogs(r.Context(), containerID, lines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fetch logs: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"logs":   output,
		"lines":  lines,
		"remote": false,
	})
}

// apiContainerLogStream streams container logs via SSE (local containers only).
func (s *Server) apiContainerLogStream(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "container name required")
		return
	}

	// Streaming is local-only; remote containers need gRPC bidirectional streaming.
	if host := r.URL.Query().Get("host"); host != "" {
		writeError(w, http.StatusNotImplemented, "log streaming is not supported for remote containers")
		return
	}

	if s.deps.LogStreamer == nil {
		writeError(w, http.StatusServiceUnavailable, "log streaming not available")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	lines := 50
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	if lines > 500 {
		lines = 500
	}

	containerID, err := s.resolveContainerID(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list containers")
		return
	}
	if containerID == "" {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}

	reader, tty, err := s.deps.LogStreamer.ContainerLogStream(r.Context(), containerID, lines)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start log stream: "+err.Error())
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	fmt.Fprint(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	if tty {
		s.streamTTYLogs(w, flusher, r.Context(), reader)
	} else {
		s.streamMuxLogs(w, flusher, r.Context(), reader)
	}

	fmt.Fprint(w, "event: eof\ndata: {}\n\n")
	flusher.Flush()
}

// streamTTYLogs reads raw line-by-line output from a TTY container.
func (s *Server) streamTTYLogs(w http.ResponseWriter, flusher http.Flusher, ctx context.Context, reader io.Reader) {
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lines:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// streamMuxLogs reads Docker's multiplexed stream format (8-byte header per frame).
// Header: byte 0 = stream type (1=stdout, 2=stderr), bytes 4-7 = payload size (big-endian).
func (s *Server) streamMuxLogs(w http.ResponseWriter, flusher http.Flusher, ctx context.Context, reader io.Reader) {
	type frame struct {
		lines []string
		err   error
	}
	frames := make(chan frame)
	go func() {
		defer close(frames)
		hdr := make([]byte, 8)
		for {
			if _, err := io.ReadFull(reader, hdr); err != nil {
				frames <- frame{err: err}
				return
			}
			size := binary.BigEndian.Uint32(hdr[4:8])
			if size == 0 {
				continue
			}
			if size > 65536 {
				size = 65536
			}
			payload := make([]byte, size)
			if _, err := io.ReadFull(reader, payload); err != nil {
				frames <- frame{err: err}
				return
			}
			text := strings.TrimRight(string(payload), "\n")
			frames <- frame{lines: strings.Split(text, "\n")}
		}
	}()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case f, ok := <-frames:
			if !ok || f.err != nil {
				return
			}
			for _, line := range f.lines {
				fmt.Fprintf(w, "data: %s\n\n", line)
			}
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// resolveContainerID looks up a container ID by name.
func (s *Server) resolveContainerID(ctx context.Context, name string) (string, error) {
	containers, err := s.deps.Docker.ListAllContainers(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range containers {
		for _, n := range c.Names {
			cname := n
			if len(cname) > 0 && cname[0] == '/' {
				cname = cname[1:]
			}
			if cname == name {
				return c.ID, nil
			}
		}
	}
	return "", nil
}
