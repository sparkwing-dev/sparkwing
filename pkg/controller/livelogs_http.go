package controller

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/streamhttp"
)

// safety: one unauthenticated-sized read per post, so a runner with
// more to say posts several batches rather than one unbounded body.
const liveLogAppendLimit = 1 << 20

// LiveLogRead is the body of a since-offset live log read.
type LiveLogRead struct {
	// Start is the offset the returned data begins at. It exceeds the
	// requested offset when the ring had already evicted those bytes.
	Start int64 `json:"start"`

	// Next is the offset to ask for on the next read.
	Next int64 `json:"next"`

	// Data is the log text between Start and Next.
	Data string `json:"data"`

	// Done reports that the node has finished writing, so Next will not
	// advance again.
	Done bool `json:"done"`
}

func (s *Server) handleAppendNodeLiveLog(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	data, err := io.ReadAll(io.LimitReader(r.Body, liveLogAppendLimit+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(data) > liveLogAppendLimit {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("live log batch exceeds %d bytes", liveLogAppendLimit))
		return
	}
	s.liveLogs.Append(runID, nodeID, data)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReadNodeLiveLog(w http.ResponseWriter, r *http.Request) {
	since, err := liveLogSince(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	chunk, ok := s.liveLogs.Read(r.PathValue("id"), r.PathValue("nodeID"), since)
	if !ok {
		writeError(w, http.StatusNotFound, errors.New("no live log buffered for this node"))
		return
	}
	writeJSON(w, http.StatusOK, LiveLogRead{
		Start: chunk.Start,
		Next:  chunk.Next,
		Data:  string(chunk.Data),
		Done:  chunk.Done,
	})
}

func (s *Server) handleStreamNodeLiveLog(w http.ResponseWriter, r *http.Request) {
	since, err := liveLogSince(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runID := r.PathValue("id")
	nodeID := r.PathValue("nodeID")
	if _, ok := s.liveLogs.Read(runID, nodeID, since); !ok {
		writeError(w, http.StatusNotFound, errors.New("no live log buffered for this node"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	out, err := streamhttp.NewWriter(w, 30*time.Second)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprintln(out, ": open"); err != nil {
		return
	}
	if err := out.Flush(); err != nil {
		return
	}

	// safety: the writer wakes this reader, so the keepalive is the only
	// thing that has to run on a clock of its own.
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	woke := make(chan struct{}, 1)
	woke <- struct{}{}

	var partial string
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprintln(out, ": keepalive"); err != nil {
				return
			}
			if err := out.Flush(); err != nil {
				return
			}
		case <-woke:
			chunk, ok := s.liveLogs.Wait(r.Context(), runID, nodeID, since)
			if !ok {
				return
			}
			since = chunk.Next
			lines, rest := splitCompleteLines(partial + string(chunk.Data))
			partial = rest
			for _, line := range lines {
				if _, err := fmt.Fprintf(out, "id: %d\ndata: %s\n\n", since, liveLogSSEEscape(line)); err != nil {
					return
				}
			}
			if len(lines) > 0 {
				if err := out.Flush(); err != nil {
					return
				}
			}
			if chunk.Done {
				fmt.Fprint(out, "event: stream_end\ndata: {}\n\n")
				_ = out.Flush()
				return
			}
			select {
			case woke <- struct{}{}:
			default:
			}
		}
	}
}

func liveLogSince(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("since"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	}
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("since must be a non-negative byte offset, got %q", raw)
	}
	return n, nil
}

func splitCompleteLines(s string) ([]string, string) {
	parts := strings.Split(s, "\n")
	return parts[:len(parts)-1], parts[len(parts)-1]
}

func liveLogSSEEscape(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", "")
}
