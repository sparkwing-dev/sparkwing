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

func writeEventsViaBackend(ctx context.Context, b backend.Backend, runID string, opts LogsOpts, out io.Writer) error {
	const page = 500
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	var after int64
	for {
		events, err := b.ListEventsAfter(ctx, runID, after, page)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			break
		}
		// safety: the contract is seq > after, ascending; a backend that
		// ignores after would otherwise replay one page forever.
		last := events[len(events)-1].Seq
		if last <= after {
			break
		}
		for _, e := range events {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		after = last
		if len(events) < page {
			break
		}
	}
	data := buf.Bytes()
	if opts.Tail > 0 || opts.Head > 0 || opts.Lines != "" || opts.Grep != "" {
		data = opts.applyClientFilters(data)
	}
	_, err := out.Write(data)
	return err
}

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
	select {
	case <-time.After(600 * time.Millisecond):
	case <-ctx.Done():
	}
	cancel()
	wg.Wait()
	return nil
}

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
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

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
			complete := lines[:len(lines)-1]
			emitted := 0
			for _, line := range complete {
				emitted += len(line) + 1
				mu.Lock()
				if multi.Load() {
					fmt.Fprintf(out, "[%s] ", nodeID)
				}
				_, _ = out.Write(line)
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

func copyNodeStream(ctx context.Context, rc io.Reader, nodeID string, multi *atomic.Bool, mu *sync.Mutex, out io.Writer) {
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
				_, _ = out.Write(line)
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
				_, _ = out.Write(partial)
				fmt.Fprintln(out)
				mu.Unlock()
			}
			return
		}
	}
}
