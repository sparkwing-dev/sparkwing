package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Severity ranks a finding. Anything at medium or above blocks the
// push; low is advisory and lands in the bucket without gating.
type Severity string

const (
	SevBlocker Severity = "blocker"
	SevHigh    Severity = "high"
	SevMedium  Severity = "medium"
	SevLow     Severity = "low"
)

var sevRank = map[Severity]int{SevBlocker: 0, SevHigh: 1, SevMedium: 2, SevLow: 3}

// blocks reports whether a finding at this severity fails the gate.
// The threshold is medium: blocker, high, and medium block; low does not.
func (s Severity) blocks() bool {
	return s == SevBlocker || s == SevHigh || s == SevMedium
}

func (s Severity) valid() bool {
	_, ok := sevRank[s]
	return ok
}

// Finding is one issue raised by one reviewer against the pushed diff.
type Finding struct {
	Agent      string   `json:"agent"`
	File       string   `json:"file"`
	Line       int      `json:"line,omitempty"`
	Severity   Severity `json:"severity"`
	Category   string   `json:"category,omitempty"`
	Claim      string   `json:"claim"`
	Suggestion string   `json:"suggestion,omitempty"`
}

// agentPayload is the JSON shape each reviewer emits (the orchestrator
// stamps Agent onto each finding afterward, so it is absent here).
type agentPayload struct {
	Findings []Finding `json:"findings"`
}

// blocking returns the subset of findings that fail the gate.
func blocking(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Severity.blocks() {
			out = append(out, f)
		}
	}
	return out
}

// sortFindings orders by severity (most severe first), then agent, then
// file/line, so the report is stable across runs.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if a, b := sevRank[fs[i].Severity], sevRank[fs[j].Severity]; a != b {
			return a < b
		}
		if fs[i].Agent != fs[j].Agent {
			return fs[i].Agent < fs[j].Agent
		}
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		return fs[i].Line < fs[j].Line
	})
}

// writeBucket persists the full finding set so a later resume run (and
// the human iterating between pushes) can see what the last review said.
func writeBucket(path string, fs []Finding) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	sortFindings(fs)
	data, err := json.MarshalIndent(fs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// report renders the human-readable summary printed by the gate.
func report(fs []Finding) string {
	if len(fs) == 0 {
		return "agent-review: no findings\n"
	}
	sortFindings(fs)
	var b strings.Builder
	cur := ""
	for _, f := range fs {
		if f.Agent != cur {
			fmt.Fprintf(&b, "\n%s\n", f.Agent)
			cur = f.Agent
		}
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(&b, "  [%s] %s — %s\n", f.Severity, loc, f.Claim)
		if f.Suggestion != "" {
			fmt.Fprintf(&b, "          ↳ %s\n", f.Suggestion)
		}
	}
	return b.String()
}
