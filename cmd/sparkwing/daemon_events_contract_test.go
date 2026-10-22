package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
)

func TestDaemonEventsRejectsLegacyAndExplainPaginationBeforeReading(t *testing.T) {
	for _, args := range [][]string{
		{"--offset", "1"},
		{"--limit", "0"},
		{"--limit", "-1"},
		{"--since", "-1h"},
		{"--incarnation", "-1"},
		{"--cursor", "malformed"},
		{"--explain"},
		{"--explain", "--run", "r", "--cursor", "opaque"},
		{"--explain", "--run", "r", "--limit", "1"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			reads := 0
			err := runDaemonEventsCommandArgs(args, &out, &diagnostics, time.Now(), func() (string, []journal.Record, int, error) {
				reads++
				return "", nil, 0, fmt.Errorf("must not read")
			})
			if err == nil || exitCodeFor(err) != 2 || out.Len() != 0 || reads != 0 {
				t.Fatalf("error=%v reads=%d output=%q", err, reads, out.String())
			}
		})
	}
}

func TestDaemonEventsHelpReportsZeroIncarnationBoundWithoutReading(t *testing.T) {
	var out, diagnostics bytes.Buffer
	err := runDaemonEventsCommandArgs([]string{"--incarnation", "0", "--help", "-o", "plain"}, &out, &diagnostics, time.Now(), func() (string, []journal.Record, int, error) {
		t.Fatal("help read the journal")
		return "", nil, 0, nil
	})
	if err != nil || !strings.Contains(out.String(), "Must be at least 0.") {
		t.Fatalf("help disagrees with bound: %s, %v", out.String(), err)
	}
}

type daemonEventsBrokenWriter struct{ err error }

func (w daemonEventsBrokenWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDaemonEventsPropagatesReadAndWriteFailures(t *testing.T) {
	failed := errors.New("unavailable bytes")
	for _, mode := range []string{"read", "json", "plain", "pretty", "explain-json", "explain-plain", "empty", "warning", "continuation"} {
		t.Run(mode, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			var output, notes io.Writer = &out, &diagnostics
			args := []string{"-o", "json", "--limit", "1"}
			records := []journal.Record{{Kind: "request", RunID: "r"}, {Kind: "grant", RunID: "r"}}
			skipped := 0
			switch mode {
			case "json", "plain", "pretty":
				args[1], output = mode, daemonEventsBrokenWriter{failed}
			case "explain-json", "explain-plain":
				args = []string{"--explain", "--run", "r", "-o", strings.TrimPrefix(mode, "explain-")}
				output = daemonEventsBrokenWriter{failed}
			case "empty":
				args = []string{"-o", "plain"}
				records, output = nil, daemonEventsBrokenWriter{failed}
			case "warning":
				skipped, notes = 1, daemonEventsBrokenWriter{failed}
			case "continuation":
				args[1], notes = "plain", daemonEventsBrokenWriter{failed}
			}
			err := runDaemonEventsCommandArgs(args, output, notes, time.Now(), func() (string, []journal.Record, int, error) {
				if mode == "read" {
					return "", nil, 0, failed
				}
				return "journal", records, skipped, nil
			})
			if !errors.Is(err, failed) {
				t.Fatalf("lost failure: %v", err)
			}
		})
	}
}

func TestDaemonEventsBoundedOutput(t *testing.T) {
	var out, notes bytes.Buffer
	records := []journal.Record{{Kind: "queued", RunID: "r"}, {Kind: "grant", RunID: "r"}, {Kind: "release", RunID: "other"}}
	err := runDaemonEventsCommandArgs([]string{"--run", "r", "--limit", "1", "-o", "json"}, &out, &notes, time.Now(), func() (string, []journal.Record, int, error) { return "journal", records, 2, nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"kind":"queued"`) || !strings.Contains(out.String(), `"kind":"grant"`) || !strings.Contains(out.String(), `"truncated":true`) || !strings.Contains(out.String(), `"next_cursor":""`) || !strings.Contains(notes.String(), "Skipped 2") {
		t.Fatalf("output=%s notes=%s", out.String(), notes.String())
	}
}

type daemonEventPage struct {
	Records   []journal.Record
	Truncated bool
}

func decodeDaemonEventPage(t *testing.T, text string) daemonEventPage {
	t.Helper()
	var page daemonEventPage
	decoder := json.NewDecoder(strings.NewReader(text))
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		var metadata struct {
			Kind      string `json:"kind"`
			Truncated bool   `json:"truncated"`
		}
		if err := json.Unmarshal(raw, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Kind == "page" {
			page.Truncated = metadata.Truncated
			return page
		}
		var record journal.Record
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		page.Records = append(page.Records, record)
	}
}
