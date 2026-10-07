package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/api"
	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/streamhttp"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func serveLogStream(b backend.Backend, w http.ResponseWriter, r *http.Request, runID, nodeID string) {
	body, err := b.StreamNodeLog(r.Context(), runID, nodeID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if body == nil {
		// safety: an object-store logs surface has no live read, so the
		// controller's in-memory ring is the only live view of a node
		// that is still running.
		live, liveErr := backend.StreamLiveLog(r.Context(), b, runID, nodeID, 0)
		if liveErr != nil || live == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return
		}
		body = live
	}
	defer body.Close()

	_, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	out, err := streamhttp.NewWriter(w, 30*time.Second)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	format := negotiateLogFormat(r)
	if format == formatRaw {
		buf := make([]byte, 4096)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				if _, werr := out.Write(buf[:n]); werr != nil {
					return
				}
				if err := out.Flush(); err != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}

	streamPrettySSE(body, out, out.Flush, format)
}

func serveEventsStream(b backend.Backend, w http.ResponseWriter, r *http.Request, runID string) {
	_, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming not supported"))
		return
	}

	run, err := b.GetRun(r.Context(), runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	afterSeq := parseLastEventID(r.Header.Get("Last-Event-ID"))

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	out, err := streamhttp.NewWriter(w, 30*time.Second)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)

	if _, err := out.Write([]byte(": open\n\n")); err != nil {
		return
	}
	if err := out.Flush(); err != nil {
		return
	}

	ctx := r.Context()
	const (
		pollInterval    = 250 * time.Millisecond
		pageSize        = 500
		runStatusEveryN = 8
		heartbeatEvery  = 20 * time.Second
	)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	tick := 0
	lastHB := time.Now()
	terminal := isRunTerminal(run.Status)

	for {
		events, err := b.ListEventsAfter(ctx, runID, afterSeq, pageSize)
		if err != nil {
			return
		}
		events = api.PublicEvents(events)
		for _, ev := range events {
			if !writeEventSSE(out, ev) {
				return
			}
			afterSeq = ev.Seq
		}
		if len(events) > 0 {
			if err := out.Flush(); err != nil {
				return
			}
			lastHB = time.Now()
		}

		if terminal && len(events) == 0 {
			if _, err := out.Write([]byte("event: stream_end\ndata: {}\n\n")); err != nil {
				return
			}
			if err := out.Flush(); err != nil {
				return
			}
			return
		}

		if time.Since(lastHB) >= heartbeatEvery {
			if _, werr := out.Write([]byte(": keepalive\n\n")); werr != nil {
				return
			}
			if err := out.Flush(); err != nil {
				return
			}
			lastHB = time.Now()
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		tick++
		if tick%runStatusEveryN == 0 && !terminal {
			if fresh, rerr := b.GetRun(ctx, runID); rerr == nil && fresh != nil {
				terminal = isRunTerminal(fresh.Status)
			}
		}
	}
}

func parseLastEventID(h string) int64 {
	if h == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(h), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func isRunTerminal(status string) bool {
	switch status {
	case "success", "failed", "cancelled":
		return true
	}
	return false
}

func writeEventSSE(w io.Writer, ev store.Event) bool {
	type wire struct {
		RunID   string          `json:"run_id"`
		Seq     int64           `json:"seq"`
		NodeID  string          `json:"node_id,omitempty"`
		Kind    string          `json:"kind"`
		TS      time.Time       `json:"ts"`
		Payload json.RawMessage `json:"payload,omitempty"`
	}
	body, err := json.Marshal(wire{
		RunID:   ev.RunID,
		Seq:     ev.Seq,
		NodeID:  ev.NodeID,
		Kind:    ev.Kind,
		TS:      ev.TS,
		Payload: ev.Payload,
	})
	if err != nil {
		return false
	}
	frame := fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Kind, body)
	_, werr := w.Write([]byte(frame))
	return werr == nil
}

func serveLogs(b backend.Backend, w http.ResponseWriter, r *http.Request, runID, nodeID string) {
	format := negotiateLogFormat(r)
	w.Header().Set("Content-Type", contentTypeFor(format))
	if nodeID != "" {
		content, err := b.ReadNodeLog(r.Context(), runID, nodeID, backend.ReadOpts{})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if len(content) == 0 {
			return
		}
		if format == formatRaw {
			_, _ = w.Write(content)
			return
		}
		renderJSONL(content, w, format)
		return
	}

	nodes, err := b.ListNodes(r.Context(), runID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for i, n := range nodes {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "=== %s (%s) ===\n", n.NodeID, n.Outcome)
		content, err := b.ReadNodeLog(r.Context(), runID, n.NodeID, backend.ReadOpts{})
		if err != nil {
			fmt.Fprintf(w, "(error: %v)\n", err)
			continue
		}
		if format == formatRaw {
			_, _ = w.Write(content)
			continue
		}
		renderJSONL(content, w, format)
	}
}

// RunLogsHandler serves every node log of the run named by the {id} path value.
func RunLogsHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveLogs(b, w, r, r.PathValue("id"), "")
	}
}

type displayLine struct {
	body string
	step string
	show bool
}

func displayBodyForLogLine(raw string) (string, bool) {
	d := parseDisplayLine(raw)
	return d.body, d.show
}

func parseDisplayLine(raw string) displayLine {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed[0] != '{' {
		return displayLine{body: raw, show: true}
	}
	var rec struct {
		Event string                 `json:"event"`
		Msg   string                 `json:"msg"`
		Level string                 `json:"level"`
		Step  string                 `json:"step"`
		Attrs map[string]interface{} `json:"attrs,omitempty"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
		return displayLine{body: raw, show: true}
	}
	switch rec.Event {
	case "node_start", "step_start", "step_end", "node_end", "run_summary":
		return displayLine{show: false}
	case "step_skipped":
		reason, _ := rec.Attrs["reason"].(string)
		if reason != "" {
			return displayLine{body: "[skipped: " + reason + "]", step: rec.Step, show: true}
		}
		return displayLine{body: "[skipped]", step: rec.Step, show: true}
	}
	if rec.Msg != "" {
		return displayLine{body: rec.Msg, step: rec.Step, show: true}
	}
	if len(rec.Attrs) > 0 {
		attrBytes, err := json.Marshal(rec.Attrs)
		if err != nil {
			return displayLine{body: raw, show: true}
		}
		return displayLine{body: string(attrBytes), step: rec.Step, show: true}
	}
	return displayLine{body: "", step: rec.Step, show: true}
}

// RunLogsSearchHandler searches the run named by {id} for the q query value.
func RunLogsSearchHandler(b backend.Backend) http.HandlerFunc {
	type match struct {
		NodeID  string `json:"node_id"`
		Line    int    `json:"line"`
		Content string `json:"content"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("id")
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeErr(w, http.StatusBadRequest, errors.New("q is required"))
			return
		}
		limit := 500
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > 5000 {
			limit = 5000
		}
		needle := strings.ToLower(q)
		nodes, err := b.ListNodes(r.Context(), runID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// perf: fan out per-node reads; each ReadNodeLog is a separate HTTP hop in cluster mode.
		type nodeResult struct {
			matches   []match
			count     int
			order     int
			truncated bool
		}
		const fanout = 8
		sem := make(chan struct{}, fanout)
		results := make([]nodeResult, len(nodes))
		var wg sync.WaitGroup
		ctx := r.Context()
		for i, n := range nodes {
			wg.Add(1)
			sem <- struct{}{}
			go func(ctx context.Context, i int, nodeID string) {
				defer wg.Done()
				defer func() { <-sem }()
				content, err := b.ReadNodeLog(ctx, runID, nodeID, backend.ReadOpts{})
				if err != nil || len(content) == 0 {
					return
				}
				sc := bufio.NewScanner(bytes.NewReader(content))
				sc.Buffer(make([]byte, 1<<16), 1<<20)
				displayLine := 0
				local := nodeResult{order: i}
				for sc.Scan() {
					raw := sc.Text()
					body, ok := displayBodyForLogLine(raw)
					if !ok {
						continue
					}
					displayLine++
					if !strings.Contains(strings.ToLower(body), needle) {
						continue
					}
					local.count++
					if local.count <= limit {
						local.matches = append(local.matches, match{
							NodeID:  nodeID,
							Line:    displayLine,
							Content: body,
						})
					}
				}
				local.truncated = errors.Is(sc.Err(), bufio.ErrTooLong)
				results[i] = local
			}(ctx, i, n.NodeID)
		}
		wg.Wait()
		matches := make([]match, 0, 64)
		total := 0
		truncated := false
		for _, res := range results {
			total += res.count
			truncated = truncated || res.truncated
			for _, m := range res.matches {
				if len(matches) >= limit {
					break
				}
				matches = append(matches, m)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"query":     q,
			"results":   matches,
			"total":     total,
			"truncated": truncated,
		})
	}
}

// RunsGrepHandler searches the logs of the runs b lists for the q query value.
func RunsGrepHandler(b backend.Backend) http.HandlerFunc {
	type match struct {
		RunID    string `json:"run_id"`
		Pipeline string `json:"pipeline"`
		NodeID   string `json:"node_id"`
		StepID   string `json:"step_id,omitempty"`
		Line     int    `json:"line"`
		Content  string `json:"content"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeErr(w, http.StatusBadRequest, errors.New("q is required"))
			return
		}
		needle := strings.ToLower(q)
		runLimit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				runLimit = n
			}
		}
		if runLimit > 1000 {
			runLimit = 1000
		}
		maxMatches := 5
		if v := r.URL.Query().Get("max_matches"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				maxMatches = n
			}
		}
		shaPrefixes := r.URL.Query()["sha"]
		for _, prefix := range shaPrefixes {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" || strings.IndexFunc(prefix, func(ch rune) bool {
				return !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F'))
			}) >= 0 {
				writeErr(w, http.StatusBadRequest, errors.New("sha must be a hexadecimal prefix"))
				return
			}
		}
		filter := store.RunFilter{
			Pipelines:      r.URL.Query()["pipeline"],
			Statuses:       r.URL.Query()["status"],
			GitBranches:    r.URL.Query()["branch"],
			GitSHAPrefixes: shaPrefixes,
			Limit:          runLimit,
		}
		if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
			if d, err := time.ParseDuration(sinceStr); err == nil && d > 0 {
				filter.Since = time.Now().Add(-d)
			}
		}
		runs, err := b.ListRuns(r.Context(), filter)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		runs = applyGrepExcludes(runs, grepExcludes{
			pipelines:   r.URL.Query()["npipeline"],
			statuses:    r.URL.Query()["nstatus"],
			branches:    r.URL.Query()["nbranch"],
			shaPrefixes: r.URL.Query()["nsha"],
		})
		if ids, specified := r.URL.Query()["run_id"]; specified {
			allowed := make(map[string]bool, len(ids))
			for _, id := range ids {
				allowed[id] = true
			}
			kept := runs[:0]
			for _, run := range runs {
				if allowed[run.ID] {
					kept = append(kept, run)
				}
			}
			runs = kept
		}
		var matches []match
		total := 0
		truncated := false
		searched := 0
		runsMeta := make(map[string]*store.Run)
		for _, run := range runs {
			if r.Context().Err() != nil || (maxMatches > 0 && len(matches) >= maxMatches) {
				truncated = true
				break
			}
			nodes, err := b.ListNodes(r.Context(), run.ID)
			if err != nil {
				truncated = true
				continue
			}
			searched++
			for _, node := range nodes {
				if maxMatches > 0 && len(matches) >= maxMatches {
					truncated = true
					break
				}
				content, err := b.ReadNodeLog(r.Context(), run.ID, node.NodeID, backend.ReadOpts{})
				if err != nil {
					truncated = true
					continue
				}
				if len(content) == 0 {
					continue
				}
				sc := bufio.NewScanner(bytes.NewReader(content))
				sc.Buffer(make([]byte, 1<<16), 1<<20)
				displayLine := 0
				for sc.Scan() {
					d := parseDisplayLine(sc.Text())
					if !d.show {
						continue
					}
					displayLine++
					if !strings.Contains(strings.ToLower(d.body), needle) {
						continue
					}
					total++
					matches = append(matches, match{RunID: run.ID, Pipeline: run.Pipeline, NodeID: node.NodeID, StepID: d.step, Line: displayLine, Content: d.body})
					runsMeta[run.ID] = store.RedactedRun(run)
					if maxMatches > 0 && len(matches) >= maxMatches {
						truncated = true
						break
					}
				}
				truncated = truncated || errors.Is(sc.Err(), bufio.ErrTooLong)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"query":         q,
			"matches":       matches,
			"runs":          runsMeta,
			"total":         total,
			"runs_scanned":  searched,
			"runs_matching": len(runs),
			"truncated":     truncated,
		})
	}
}

type grepExcludes struct {
	pipelines   []string
	statuses    []string
	branches    []string
	shaPrefixes []string
}

func applyGrepExcludes(runs []*store.Run, ex grepExcludes) []*store.Run {
	if len(ex.pipelines)+len(ex.statuses)+len(ex.branches)+len(ex.shaPrefixes) == 0 {
		return runs
	}
	out := runs[:0]
	for _, run := range runs {
		if containsExact(ex.pipelines, run.Pipeline) {
			continue
		}
		if containsExact(ex.statuses, run.Status) {
			continue
		}
		if containsExact(ex.branches, run.GitBranch) {
			continue
		}
		excludedBySHA := false
		for _, p := range ex.shaPrefixes {
			if p != "" && strings.HasPrefix(run.GitSHA, p) {
				excludedBySHA = true
				break
			}
		}
		if excludedBySHA {
			continue
		}
		out = append(out, run)
	}
	return out
}

func containsExact(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// NodeLogsHandler serves the log of the {node} of run {id}.
func NodeLogsHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveLogs(b, w, r, r.PathValue("id"), r.PathValue("node"))
	}
}

// NodeLogCompletenessHandler reports whether the log of {node} in run {id} is whole.
func NodeLogCompletenessHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID, nodeID := r.PathValue("id"), r.PathValue("node")
		nodes, err := b.ListNodes(r.Context(), runID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		i := slices.IndexFunc(nodes, func(n *store.Node) bool { return n.NodeID == nodeID })
		if i < 0 {
			writeErr(w, http.StatusNotFound, fmt.Errorf("node %s not found in run %s", nodeID, runID))
			return
		}
		c, err := backend.NodeLogCompleteness(r.Context(), b, runID, nodes[i], time.Now())
		if err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	}
}

// NodeLogStreamHandler streams the log of {node} in run {id} as server-sent events.
func NodeLogStreamHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveLogStream(b, w, r, r.PathValue("id"), r.PathValue("node"))
	}
}

// EventsStreamHandler streams the events of run {id} as server-sent events.
func EventsStreamHandler(b backend.Backend) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serveEventsStream(b, w, r, r.PathValue("id"))
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// safety: the status is already sent, so a client that went away mid-body is the only failure left to see.
	if err := json.NewEncoder(w).Encode(body); err != nil {
		return
	}
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
