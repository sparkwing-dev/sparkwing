package controller

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// safety: one unauthenticated-sized read per post, so a runner with
// more to say posts several batches rather than one unbounded body.
const liveLogAppendLimit = 1 << 20

const liveLogWaitSlice = 5 * time.Second

const liveLogKeepalive = 15 * time.Second

func streamLiveLog(ctx context.Context, l *liveLogs, out liveLogSink, runID, nodeID string, since int64) error {
	if _, err := fmt.Fprintln(out, ": open"); err != nil {
		return err
	}
	if err := out.Flush(); err != nil {
		return err
	}

	lastWrite := time.Now()
	var partial string
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.Watch(runID, nodeID)
		// safety: the wait is sliced so a silent node still reaches the
		// keepalive below; an idle stream with no bytes is one an
		// intermediary closes.
		waitCtx, cancel := context.WithTimeout(ctx, liveLogWaitSlice)
		want := since
		chunk, ok := l.Wait(waitCtx, runID, nodeID, want)
		cancel()
		if !ok {
			if ctx.Err() == nil {
				return emitLiveLogSSE(out, since, liveLogReleasedMarker)
			}
			return nil
		}
		if chunk.Start > want {
			if err := emitLiveLogSSE(out, chunk.Start, liveLogGapMarker(chunk.Start-want)); err != nil {
				return err
			}
			partial = ""
		}
		since = chunk.Next

		lines, rest := splitCompleteLines(partial + string(chunk.Data))
		partial = rest
		for _, line := range lines {
			if err := writeLiveLogSSE(out, since, line); err != nil {
				return err
			}
		}
		if len(lines) > 0 {
			if err := out.Flush(); err != nil {
				return err
			}
			lastWrite = time.Now()
		}
		if chunk.Done {
			if _, err := fmt.Fprint(out, "event: stream_end\ndata: {}\n\n"); err != nil {
				return err
			}
			return out.Flush()
		}
		if time.Since(lastWrite) >= liveLogKeepalive {
			if _, err := fmt.Fprintln(out, ": keepalive"); err != nil {
				return err
			}
			if err := out.Flush(); err != nil {
				return err
			}
			lastWrite = time.Now()
		}
	}
}

func splitCompleteLines(s string) ([]string, string) {
	parts := strings.Split(s, "\n")
	return parts[:len(parts)-1], parts[len(parts)-1]
}

// safety: a reader whose buffer vanished has to be told, or a truncated
// live view reads as a complete one.
const liveLogReleasedMarker = `{"level":"warn","msg":"sparkwing released this node's live log buffer; read the durable copy"}`

func liveLogGapMarker(dropped int64) string {
	return fmt.Sprintf(
		`{"level":"warn","msg":"sparkwing dropped %d byte(s) of this node's live log before this point; the durable copy has them"}`,
		dropped)
}

type liveLogSink interface {
	io.Writer
	Flush() error
}

func writeLiveLogSSE(out io.Writer, id int64, line string) error {
	_, err := fmt.Fprintf(out, "id: %d\ndata: %s\n\n", id, liveLogSSEEscape(line))
	return err
}

// safety: a marker the reader has to see is flushed with its event, so
// it does not sit in the buffer behind the next wait.
func emitLiveLogSSE(out liveLogSink, id int64, line string) error {
	if err := writeLiveLogSSE(out, id, line); err != nil {
		return err
	}
	return out.Flush()
}

func liveLogSSEEscape(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\r", "")
}
