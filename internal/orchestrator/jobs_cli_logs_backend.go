package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// writeLogsViaBackend renders per-node log output via a Backend. Used
// for Mode 2 (S3 / object-store) and Mode 4 (controller) state, where
// the local on-disk envelope + per-node files don't exist. The per-
// node body is already NDJSON of [sparkwing.LogRecord], so this is
// basically a per-node ReadNodeLog plus the same banner / format
// rules as [writeLogsTextRemote].
func writeLogsViaBackend(ctx context.Context, b backend.Backend, runID string, target []*store.Node, opts LogsOpts, out io.Writer) error {
	filter := backend.ReadOpts{
		Tail:  opts.Tail,
		Head:  opts.Head,
		Lines: opts.Lines,
		Grep:  opts.Grep,
	}
	jsonOut := opts.JSON || opts.Format == "json"
	for i, n := range target {
		if len(target) > 1 && !jsonOut {
			if i > 0 {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "=== %s (%s) ===\n", n.NodeID, orDash(n.Outcome))
		}
		if n.StartedAt == nil {
			if len(target) > 1 && !jsonOut {
				fmt.Fprintln(out, "(did not execute)")
			}
			continue
		}
		data, err := b.ReadNodeLog(ctx, runID, n.NodeID, filter)
		if err != nil {
			return fmt.Errorf("read %s: %w", n.NodeID, err)
		}
		if len(data) > 0 && data[0] == '{' {
			if err := renderJSONLStream(bytes.NewReader(data), opts, out); err != nil {
				return err
			}
			continue
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// writeEventsViaBackend renders the run's lifecycle event stream from
// the state backend (s3state for Mode 2; controller HTTP for Mode 4).
// One JSON-encoded [store.Event] per line. Used for --events-only;
// per-node body bytes go through writeLogsViaBackend.
func writeEventsViaBackend(ctx context.Context, b backend.Backend, runID string, opts LogsOpts, out io.Writer) error {
	events, err := b.ListEventsAfter(ctx, runID, 0, 0)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	data := buf.Bytes()
	if opts.Tail > 0 || opts.Head > 0 || opts.Lines != "" || opts.Grep != "" {
		data = opts.applyClientFilters(data)
	}
	_, err = out.Write(data)
	return err
}

// followLogsViaBackend tails per-node body output by polling
// Backend.ListNodes for new nodes and streaming each via
// Backend.StreamNodeLog. Mirrors [followLogsRemote] for the
// backend-abstracted code path. Exits on run-terminal status (with
// a brief drain window) or ctx cancel.
func followLogsViaBackend(ctx context.Context, b backend.Backend, runID, nodeFilter string, out io.Writer) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var writeMu sync.Mutex
	seen := map[string]struct{}{}
	var wg sync.WaitGroup
	var multi atomic.Bool

	spawn := func(nodeID string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			streamNodeViaBackend(runCtx, b, runID, nodeID, &multi, &writeMu, out)
		}()
	}

	terminal := make(chan struct{})

	go func() {
		defer close(terminal)
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				nodes, err := b.ListNodes(runCtx, runID)
				if err == nil {
					for _, n := range nodes {
						if nodeFilter != "" && n.NodeID != nodeFilter {
							continue
						}
						if _, ok := seen[n.NodeID]; ok {
							continue
						}
						seen[n.NodeID] = struct{}{}
						if len(seen) > 1 {
							multi.Store(true)
						}
						spawn(n.NodeID)
					}
				}
				run, err := b.GetRun(runCtx, runID)
				if err == nil && isTerminalStatus(run.Status) {
					return
				}
			}
		}
	}()

	<-terminal
	// Drain window so streams flush final lines.
	select {
	case <-time.After(600 * time.Millisecond):
	case <-ctx.Done():
	}
	cancel()
	wg.Wait()
	return nil
}

// streamNodeViaBackend reads one node's log stream from a Backend.
// Two code paths:
//
//   - StreamNodeLog returns a non-nil ReadCloser: dispatch to the
//     copy loop and reconnect on close. Used by backends with native
//     streaming (controller SSE).
//   - StreamNodeLog returns (nil, nil): poll ReadNodeLog with a
//     growing offset, emit only the new bytes each cycle. Used by
//     backends like S3 that have no real stream API but expose a
//     read that returns the current full body.
//
// Exits cleanly on ctx done. Reconnect/poll spacing is 250-500ms so
// a dead node doesn't tight-loop.
func streamNodeViaBackend(ctx context.Context, b backend.Backend, runID, nodeID string,
	multi *atomic.Bool, mu *sync.Mutex, out io.Writer,
) {
	for {
		rc, err := b.StreamNodeLog(ctx, runID, nodeID)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		if rc == nil {
			pollNodeViaBackend(ctx, b, runID, nodeID, multi, mu, out)
			return
		}
		copyNodeStream(ctx, rc, nodeID, multi, mu, out)
		_ = rc.Close()
		if ctx.Err() != nil {
			return
		}
		// EOF without ctx-done usually means the stream closed for
		// non-fatal reasons (TTL, paginated chunk boundary). Pause
		// briefly so we don't tight-loop reconnect on a dead node.
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// pollNodeViaBackend tails a node's log by re-reading the full body
// each cycle and emitting only the new bytes since last read. Used
// when the backend has no native stream API (S3). Exits on ctx done.
//
// Cost: each poll re-reads the full node body. For agent use against
// short-to-medium runs this is fine; the alternative (listing new
// log-shard objects since the last call) would require backend
// surface we don't have today. Revisit if log volumes warrant it.
func pollNodeViaBackend(ctx context.Context, b backend.Backend, runID, nodeID string,
	multi *atomic.Bool, mu *sync.Mutex, out io.Writer,
) {
	var lastLen int
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := b.ReadNodeLog(ctx, runID, nodeID, backend.ReadOpts{})
		if err == nil && len(data) > lastLen {
			newBytes := data[lastLen:]
			lines := bytes.Split(newBytes, []byte{'\n'})
			// Last element is partial-or-empty; flush only complete
			// lines this cycle, the rest catches up next poll.
			complete := lines[:len(lines)-1]
			emitted := 0
			for _, line := range complete {
				emitted += len(line) + 1
				mu.Lock()
				if multi.Load() {
					fmt.Fprintf(out, "[%s] ", nodeID)
				}
				out.Write(line)
				fmt.Fprintln(out)
				mu.Unlock()
			}
			lastLen += emitted
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// copyNodeStream reads NDJSON lines from rc and writes them under mu
// so concurrent nodes don't corrupt each other's output. When multi
// flips true mid-stream, each subsequent line gets a "[nodeID] "
// prefix so the operator can attribute interleaved output.
func copyNodeStream(ctx context.Context, rc io.Reader, nodeID string, multi *atomic.Bool, mu *sync.Mutex, out io.Writer) {
	// Naive line-buffered read so partial reads don't drop lines.
	// 64KB buffer matches the storage layer's typical write chunking.
	const bufSize = 64 * 1024
	buf := make([]byte, bufSize)
	var partial []byte
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := rc.Read(buf)
		if n > 0 {
			combined := append(partial, buf[:n]...)
			lines := bytes.Split(combined, []byte{'\n'})
			partial = lines[len(lines)-1]
			for _, line := range lines[:len(lines)-1] {
				mu.Lock()
				if multi.Load() {
					fmt.Fprintf(out, "[%s] ", nodeID)
				}
				out.Write(line)
				fmt.Fprintln(out)
				mu.Unlock()
			}
		}
		if err != nil {
			if len(partial) > 0 {
				mu.Lock()
				if multi.Load() {
					fmt.Fprintf(out, "[%s] ", nodeID)
				}
				out.Write(partial)
				fmt.Fprintln(out)
				mu.Unlock()
			}
			return
		}
	}
}
