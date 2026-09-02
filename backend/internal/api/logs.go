package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/calebgrabowski/overworld-ops/backend/internal/docker"
)

// handleServerLogs streams container output to the browser as Server-Sent
// Events. Each event is a JSON {stream, text} object.
//
// SSE over websockets because the console is read-only for now, EventSource
// reconnects on its own, and it needs no extra dependency on either side.
func (s *Server) handleServerLogs(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if srv.ContainerID == "" {
		writeError(w, http.StatusConflict, "server has no container yet")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	tail := r.URL.Query().Get("tail")
	if tail == "" {
		tail = "200"
	}

	rc, err := s.docker.Logs(r.Context(), srv.ContainerID, true, tail)
	if err != nil {
		if errors.Is(err, docker.ErrNotFound) {
			writeError(w, http.StatusNotFound, "container not found")
			return
		}
		slog.Error("open log stream", "server", srv.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not open log stream")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Disable proxy buffering, which otherwise holds events until a buffer
	// fills and makes the console look frozen.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	lines := make(chan docker.LogLine, 64)
	go func() {
		// DemuxLogs closes rc. Cancelling the request context unblocks it.
		if err := docker.DemuxLogs(r.Context(), rc, lines); err != nil && !errors.Is(err, r.Context().Err()) {
			slog.Debug("log stream ended", "server", srv.ID, "error", err)
		}
		close(lines)
	}()

	// A periodic comment keeps intermediaries from timing out an idle stream —
	// a stopped Minecraft server produces no output at all.
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case line, open := <-lines:
			if !open {
				return
			}
			payload, err := json.Marshal(line)
			if err != nil {
				continue
			}
			if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
				return // client went away
			}
			flusher.Flush()

		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()

		case <-r.Context().Done():
			return
		}
	}
}
