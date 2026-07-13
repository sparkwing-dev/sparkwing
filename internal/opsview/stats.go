package opsview

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// RenderStats writes the daemon's rolling admission-outcome window (the data
// behind the queue view's recent-events summary) in the requested format:
// "json" (the raw window, or null when empty), "plain" (the one-line summary),
// or pretty (a daemon header plus the summary).
func RenderStats(w io.Writer, qs wingwire.QueueState, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(qs.Events)
	case "plain":
		if line := FmtEventsLine(qs.Events); line != "" {
			fmt.Fprintln(w, line)
		}
		return nil
	default:
		if d := FmtDaemonHeader(qs); d != "" {
			fmt.Fprintln(w, d)
		}
		line := FmtEventsLine(qs.Events)
		if line == "" {
			fmt.Fprintln(w, "no admission activity recorded")
			return nil
		}
		fmt.Fprintln(w, line)
		return nil
	}
}
