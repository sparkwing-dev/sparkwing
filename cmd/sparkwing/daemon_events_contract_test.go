package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
)

type daemonEventPage struct {
	Records   []journal.Record
	Cursor    string
	Truncated bool
}

func decodeDaemonEventPage(t *testing.T, text string) daemonEventPage {
	t.Helper()
	var page daemonEventPage
	decoder := json.NewDecoder(strings.NewReader(text))
	for {
		var row json.RawMessage
		if err := decoder.Decode(&row); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		var meta struct {
			Kind       string     `json:"kind"`
			NextCursor string     `json:"next_cursor"`
			Truncated  bool       `json:"truncated"`
			TS         *time.Time `json:"ts"`
		}
		if err := json.Unmarshal(row, &meta); err != nil {
			t.Fatal(err)
		}
		if meta.Kind == "page" && meta.TS == nil {
			page.Cursor, page.Truncated = meta.NextCursor, meta.Truncated
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				t.Fatalf("page metadata was not final: %v", err)
			}
			return page
		}
		var record journal.Record
		if err := json.Unmarshal(row, &record); err != nil {
			t.Fatal(err)
		}
		page.Records = append(page.Records, record)
	}
	t.Fatal("missing final page metadata")
	return page
}

func TestDaemonEventsCursorKeepsHistoryAcrossAppends(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	dir := filepath.Join(home, "wingd")
	for i := range 7 {
		if err := appendDaemonRecord(dir, journal.Record{Kind: "queued", RunID: "selected", Data: map[string]any{"index": i}}); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--run", "selected", "--kind", "queued", "--limit", "3", "-o", "json"}
	read := func(cursor string) daemonEventPage {
		t.Helper()
		invocation := append([]string(nil), args...)
		if cursor != "" {
			invocation = append(invocation, "--cursor", cursor)
		}
		return decodeDaemonEventPage(t, captureDaemonOutput(t, func() error { return runDaemonEvents(invocation) }))
	}
	first := read("")
	if len(first.Records) != 3 || first.Cursor == "" || !first.Truncated {
		t.Fatalf("first: %+v", first)
	}
	for _, run := range []string{"selected", "other"} {
		if err := appendDaemonRecord(dir, journal.Record{Kind: "queued", RunID: run, Data: map[string]any{"index": 99}}); err != nil {
			t.Fatal(err)
		}
	}
	second := read(first.Cursor)
	third := read(second.Cursor)
	var indexes []int
	for _, page := range []daemonEventPage{third, second, first} {
		for _, record := range page.Records {
			indexes = append(indexes, int(record.Data["index"].(float64)))
		}
	}
	if !reflect.DeepEqual(indexes, []int{0, 1, 2, 3, 4, 5, 6}) || third.Truncated || third.Cursor != "" {
		t.Fatalf("continued history = %v, last=%+v", indexes, third)
	}
}

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
			err := runDaemonEventsCommand(args, &out, &diagnostics, time.Now(), func() (string, []journal.Record, int, error) {
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
	err := runDaemonEventsCommand([]string{"--incarnation", "0", "--help", "-o", "plain"}, &out, &diagnostics, time.Now(), func() (string, []journal.Record, int, error) {
		t.Fatal("help read the journal")
		return "", nil, 0, nil
	})
	if err != nil || !strings.Contains(out.String(), "Must be at least 0.") {
		t.Fatalf("help disagrees with bound: %s, %v", out.String(), err)
	}
}

func TestDaemonEventsHumanPagesExposeUsableCursor(t *testing.T) {
	for _, format := range []string{"plain", "pretty"} {
		t.Run(format, func(t *testing.T) {
			rows := []journal.Record{{Kind: "queued", RunID: "r"}, {Kind: "grant", RunID: "r"}}
			read := func() (string, []journal.Record, int, error) { return "journal", rows, 0, nil }
			var out, diagnostics bytes.Buffer
			args := []string{"--run", "r", "--limit", "1", "-o", format}
			if err := runDaemonEventsCommand(args, &out, &diagnostics, time.Now(), read); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "grant") || strings.Contains(out.String(), "cursor") || !strings.Contains(diagnostics.String(), "--cursor") {
				t.Fatalf("out=%q diagnostics=%q", out.String(), diagnostics.String())
			}
			fields := strings.Fields(diagnostics.String())
			args = append(args, "--cursor", fields[len(fields)-1])
			out.Reset()
			diagnostics.Reset()
			if err := runDaemonEventsCommand(args, &out, &diagnostics, time.Now(), read); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "queued") || diagnostics.Len() != 0 {
				t.Fatalf("last out=%q diagnostics=%q", out.String(), diagnostics.String())
			}
		})
	}
}

func TestDaemonEventsCursorKeepsSinceCutoffAndKinds(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	records := []journal.Record{
		{TS: now.Add(-48 * time.Hour), Kind: "queued", RunID: "r"},
		{TS: now.Add(-24 * time.Hour), Kind: "queued", RunID: "r", Incarnation: 7},
		{TS: now.Add(-time.Hour), Kind: "grant", DisplayRunID: "r", Incarnation: 7},
		{TS: now, Kind: "other", RunID: "r", Incarnation: 7},
	}
	read := func() (string, []journal.Record, int, error) { return "journal", records, 0, nil }
	var out, diagnostics bytes.Buffer
	args := []string{"--run", "r", "--kind", "queued,grant", "--since", "1d", "--incarnation", "7", "--limit", "1", "-o", "json"}
	if err := runDaemonEventsCommand(args, &out, &diagnostics, now, read); err != nil {
		t.Fatal(err)
	}
	first := decodeDaemonEventPage(t, out.String())
	if len(first.Records) != 1 || first.Records[0].Kind != "grant" || first.Cursor == "" {
		t.Fatalf("first: %+v", first)
	}
	args = []string{"--run", "r", "--kind", "grant", "--kind", "queued,grant", "--since", "24h", "--incarnation", "7", "--limit", "2", "--cursor", first.Cursor, "-o", "json"}
	out.Reset()
	if err := runDaemonEventsCommand(args, &out, &diagnostics, now.Add(24*time.Hour), read); err != nil {
		t.Fatal(err)
	}
	second := decodeDaemonEventPage(t, out.String())
	if len(second.Records) != 1 || second.Records[0].Kind != "queued" || second.Cursor != "" {
		t.Fatalf("cutoff shifted: %+v", second)
	}
}

func TestDaemonEventsCursorRefusesChangedUnreadHistoryAndSelection(t *testing.T) {
	now := time.Now().UTC()
	rows := []journal.Record{{TS: now, Kind: "queued", RunID: "r", Seq: 1}, {TS: now, Kind: "grant", RunID: "r", Seq: 2}, {TS: now, Kind: "release", RunID: "r", Seq: 3}}
	for _, change := range []string{"removed", "edited", "reordered", "late append", "directory", "run", "kind", "incarnation", "since"} {
		t.Run(change, func(t *testing.T) {
			records := append([]journal.Record(nil), rows...)
			dir := "journal"
			read := func() (string, []journal.Record, int, error) { return dir, records, 0, nil }
			var out, diagnostics bytes.Buffer
			args := []string{"--run", "r", "--limit", "1", "-o", "json"}
			if err := runDaemonEventsCommand(args, &out, &diagnostics, now, read); err != nil {
				t.Fatal(err)
			}
			first := decodeDaemonEventPage(t, out.String())
			args = append(args, "--cursor", first.Cursor)
			switch change {
			case "removed":
				records = records[1:]
			case "edited":
				records[0].Kind = "changed"
			case "reordered":
				records[0], records[1] = records[1], records[0]
			case "late append":
				records = append([]journal.Record{{TS: now.Add(-time.Minute), Kind: "queued", RunID: "r"}}, records...)
			case "directory":
				dir = "another-journal"
			case "run":
				args[1] = "other"
			case "kind":
				args = append(args, "--kind", "grant")
			case "incarnation":
				args = append(args, "--incarnation", "1")
			case "since":
				args = append(args, "--since", "1h")
			}
			out.Reset()
			err := runDaemonEventsCommand(args, &out, &diagnostics, now, read)
			if err == nil || out.Len() != 0 {
				t.Fatalf("changed continuation succeeded: %q, %v", out.String(), err)
			}
			want := 2
			if change == "removed" || change == "edited" || change == "reordered" || change == "late append" {
				want = 1
			}
			if exitCodeFor(err) != want {
				t.Fatalf("exit=%d, want=%d: %v", exitCodeFor(err), want, err)
			}
		})
	}
}

type daemonEventsBrokenWriter struct{ err error }

func (w daemonEventsBrokenWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDaemonEventsPropagatesReadAndWriteFailures(t *testing.T) {
	failed := errors.New("unavailable bytes")
	for _, mode := range []string{"read", "json", "plain", "pretty", "explain-json", "explain-plain", "empty", "warning", "continuation", "metadata"} {
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
			case "metadata":
				records, output = nil, daemonEventsBrokenWriter{failed}
			}
			err := runDaemonEventsCommand(args, output, notes, time.Now(), func() (string, []journal.Record, int, error) {
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

func FuzzDaemonEventsPagesPreserveRetainedHistory(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5}, uint8(2))
	f.Add([]byte{7, 7, 7, 7, 7}, uint8(1))
	f.Fuzz(func(t *testing.T, values []byte, width uint8) {
		if len(values) > 256 {
			values = values[:256]
		}
		now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
		var records []journal.Record
		for _, value := range values {
			records = append(records, journal.Record{TS: now, Kind: "queued", RunID: "r", Data: map[string]any{"value": value}})
		}
		want := append([]journal.Record(nil), records...)
		limit := int(width)%32 + 1
		query := daemonEventQuery{Run: "r"}
		var cursor daemonEventCursor
		var got []journal.Record
		for pageNumber := 0; ; pageNumber++ {
			if pageNumber > len(want) {
				t.Fatal("cursor did not progress")
			}
			page, err := selectDaemonEventPage(records, "journal", query, limit, cursor, now)
			if err != nil {
				t.Fatal(err)
			}
			got = append(append([]journal.Record(nil), page.records...), got...)
			if page.next == "" {
				break
			}
			next, err := decodeDaemonEventCursor(page.next)
			if err != nil || cursor.Remaining > 0 && next.Remaining >= cursor.Remaining {
				t.Fatalf("cursor failed to progress: %+v, %v", next, err)
			}
			cursor = next
			records = append(records, journal.Record{TS: now.Add(time.Hour), Kind: "queued", RunID: "r"})
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("changed history: got=%+v, want=%+v", got, want)
		}
	})
}

func FuzzDaemonEventCursorDecode(f *testing.F) {
	for _, seed := range []string{"", "not-a-cursor", "e30", "bnVsbA", "W10"} {
		f.Add(seed)
	}
	raw, err := json.Marshal(daemonEventCursor{Version: 1, Query: strings.Repeat("a", 64), Remaining: 1, Prefix: strings.Repeat("b", 64)})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(base64.RawURLEncoding.EncodeToString(raw))
	f.Fuzz(func(t *testing.T, value string) {
		cursor, err := decodeDaemonEventCursor(value)
		if err != nil || value == "" {
			return
		}
		if cursor.Version != 1 || cursor.Remaining <= 0 || len(cursor.Prefix) != 64 || len(cursor.Query) != 64 {
			t.Fatalf("invalid accepted cursor: %+v", cursor)
		}
	})
}
