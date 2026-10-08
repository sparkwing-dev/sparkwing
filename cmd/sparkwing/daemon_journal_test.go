package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
)

func captureDaemonOutput(t *testing.T, run func() error) string {
	t.Helper()
	return captureDaemonStream(t, &os.Stdout, run)
}

func captureDaemonErrorOutput(t *testing.T, run func() error) string {
	t.Helper()
	return captureDaemonStream(t, &os.Stderr, run)
}

func captureDaemonStream(t *testing.T, stream **os.File, run func() error) string {
	t.Helper()
	// bug: A synchronous pipe capture blocks when output exceeds the native pipe capacity.
	file, err := os.CreateTemp(t.TempDir(), "daemon-output-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	old := *stream
	*stream = file
	defer func() { *stream = old }()
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestDaemonExplainAndEventsReadWithoutDaemon(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "wingd")
	for _, record := range []journal.Record{
		{Kind: "request", RunID: "run-1", Data: map[string]any{"class": "interactive"}},
		{Kind: "queued", RunID: "run-1", Data: map[string]any{"position": 1, "blocker": "run-0", "blocking_reason": "cores"}},
		{Kind: "grant", RunID: "run-1", Data: map[string]any{"wait_ms": 350, "backfill": false}},
	} {
		if err := journal.AppendSupervisor(dir, record); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SPARKWING_HOME", home)
	plain := captureDaemonOutput(t, func() error { return runDaemonEvents([]string{"--explain", "--run", "run-1", "-o", "plain"}) })
	if !strings.Contains(plain, "Queued behind run run-0: cores") || !strings.Contains(plain, "Admitted after 350ms") {
		t.Fatalf("timeline: %s", plain)
	}
	t.Setenv("SPARKWING_HOME", home)
	jsonExplain := captureDaemonOutput(t, func() error { return runDaemonEvents([]string{"--explain", "--run", "run-1"}) })
	if strings.Count(jsonExplain, "\n") != 3 || !strings.Contains(jsonExplain, `"kind":"request"`) {
		t.Fatalf("piped explanation: %s", jsonExplain)
	}
	json := captureDaemonOutput(t, func() error {
		t.Setenv("SPARKWING_HOME", home)
		return runDaemonEvents([]string{"--run", "run-1", "--kind", "queued", "-o", "json"})
	})
	if strings.Count(json, "\n") != 1 || !strings.Contains(json, `"kind":"queued"`) {
		t.Fatalf("events: %s", json)
	}
}

func TestDaemonEventsReportsEmptyJournalLocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	plain := captureDaemonOutput(t, func() error { return runDaemonEvents([]string{"-o", "plain"}) })
	if !strings.Contains(plain, "No events are retained in "+filepath.Join(home, "wingd")) {
		t.Fatalf("empty journal: %q", plain)
	}
	t.Setenv("SPARKWING_HOME", home)
	json := captureDaemonOutput(t, func() error { return runDaemonEvents([]string{"-o", "json"}) })
	if json != "" {
		t.Fatalf("empty JSON journal: %q", json)
	}
	t.Setenv("SPARKWING_HOME", home)
	if err := runDaemonEvents([]string{"-o", "csv"}); err == nil || !strings.Contains(err.Error(), "pretty|json|plain") {
		t.Fatalf("invalid output mode: %v", err)
	}
}

func TestDaemonExplainIncludesOwnedSlotsAndChildAttaches(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "wingd")
	for _, record := range []journal.Record{
		{Kind: "request", RunID: "node-slot", Data: map[string]any{"resources": map[string]any{"cores": 0.2}, "class": "batch", "priority": 0}},
		{Kind: "grant", RunID: "node-slot", Data: map[string]any{"owner_run_id": "run-1", "wait_ms": 12400, "backfill": true}},
		{Kind: "child_attach_request", RunID: "child-1", Data: map[string]any{"requested_parent": "run-1"}},
		{Kind: "child_attach", RunID: "child-1", Data: map[string]any{"resolved_parent": "run-1"}},
		{Kind: "child_attach_request", RunID: "child-2", Data: map[string]any{"requested_parent": "run-1"}},
		{Kind: "rejected", RunID: "child-2", Data: map[string]any{"reason": "parent lease missing"}},
		{Kind: "request", RunID: "refused-slot", Data: map[string]any{"requested_owner_run_id": "run-1", "resources": map[string]any{"cores": 0.5}, "class": "batch", "priority": 0}},
		{Kind: "rejected", RunID: "refused-slot", Data: map[string]any{"reason": "draining"}},
		{Kind: "release", RunID: "node-slot", Data: map[string]any{"lease_id": "lease-1"}},
		{Kind: "grant", RunID: "other-slot", Data: map[string]any{"owner_run_id": "other-run"}},
	} {
		if err := journal.AppendSupervisor(dir, record); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SPARKWING_HOME", home)
	plain := captureDaemonOutput(t, func() error { return runDaemonEvents([]string{"--explain", "--run", "run-1", "-o", "plain"}) })
	for _, fragment := range []string{"node-slot: Requested 0.2 cores", "node-slot: Admitted after 12.4s (backfill)", "child-1: Attached child", "child-2: Request rejected: parent lease missing", "refused-slot: Request rejected: draining", "node-slot: Released lease-1"} {
		if !strings.Contains(plain, fragment) {
			t.Errorf("missing %q in %s", fragment, plain)
		}
	}
	if strings.Contains(plain, "other-slot") {
		t.Fatalf("unrelated slot included: %s", plain)
	}
}

func TestDaemonExplainFollowsDescendantsTransitively(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "wingd")
	for _, record := range []journal.Record{
		{Kind: "grant", RunID: "grandchild", Data: map[string]any{"wait_ms": 3}},
		{Kind: "request", RunID: "grandchild", Data: map[string]any{"requested_parent": "child"}},
		{Kind: "request", RunID: "child", Data: map[string]any{"owner_run_id": "root"}},
		{Kind: "grant", RunID: "unrelated", Data: map[string]any{"wait_ms": 5}},
	} {
		if err := journal.AppendSupervisor(dir, record); err != nil {
			t.Fatal(err)
		}
	}
	output := captureDaemonOutput(t, func() error {
		t.Setenv("SPARKWING_HOME", home)
		return runDaemonEvents([]string{"--explain", "--run", "root", "-o", "json"})
	})
	if strings.Count(output, `"run_id":"grandchild"`) != 2 || !strings.Contains(output, `"run_id":"child"`) || strings.Contains(output, `"run_id":"unrelated"`) {
		t.Fatalf("transitive timeline: %s", output)
	}
}

func TestDaemonJournalReportsSkippedRecords(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "wingd")
	if err := journal.AppendSupervisor(dir, journal.Record{Kind: "grant", RunID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "supervisor-events.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("bad json\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_HOME", home)
	for _, run := range []func() error{
		func() error { return runDaemonEvents([]string{"-o", "json"}) },
		func() error { return runDaemonEvents([]string{"--explain", "--run", "run-1", "-o", "json"}) },
	} {
		warning := captureDaemonErrorOutput(t, func() error {
			_ = captureDaemonOutput(t, run)
			return nil
		})
		if !strings.Contains(warning, "Skipped 1 unreadable journal records") {
			t.Fatalf("skip warning: %q", warning)
		}
	}
}

func TestExplainEventRendersKnownKindsWithoutRawJSON(t *testing.T) {
	data := map[string]any{"reason": "capacity", "role": "holding admission", "lease_id": "lease-1", "wait_ms": float64(12400), "backfill": true, "blocking_reason": "waiting for 2.0 cores", "blocker": "run-X", "resources": map[string]any{"cores": 0.2}, "class": "batch", "priority": float64(0), "requested_parent": "run-X", "resolved_parent": "run-X", "requesting_pid": float64(123), "pid_known": true, "affected_runs": []any{"run-X"}, "by_run": "run-X", "version": "v1", "socket": "/tmp/socket", "successor_version": "v2", "target_cores": float64(2), "error": "timeout", "duration_ms": float64(100), "failed_probes": float64(3), "message_type": "bad", "count": float64(4)}
	cases := map[string]string{
		"request": "Requested 0.2 cores", "queued": "Queued behind run run-X", "grant": "Admitted after 12.4s (backfill)",
		"connection_opened": "Connection opened by pid 123", "connection_handshake": "Connection handshake completed", "connection_closed": "Connection closed",
		"child_attach_request": "Requested child attachment", "child_attach": "Attached child", "reattach_request": "Requested lease reattachment", "reattach_accepted": "Reattached after daemon replacement (incarnation 3)", "reattach_refused": "Reattachment refused",
		"cancel": "Cancelled by pid 123", "release": "Released lease-1", "superseded": "Superseded by run run-X", "denied": "Admission denied", "rejected": "Request rejected", "rejection": "Invalid request rejected", "eviction": "Evicted", "queue_timeout": "Queue wait expired", "cancellation": "Admission cancelled", "backfill": "Backfilled", "reprioritize": "Priority changed", "contended": "Ran under contention", "grace_expiry": "Reattachment grace expired",
		"start": "Daemon started", "ready": "Daemon ready", "shutdown": "Daemon stopped", "drain_begin": "Daemon began draining", "drain_end": "Daemon finished draining", "headroom_sample": "Available capacity sampled", "probe_failure_start": "Supervisor probe failures began", "probe_failure_end": "Supervisor probe failures ended", "replacement": "Supervisor replaced", "handshake_refused": "Connection handshake refused", "message_refused": "Message refused", "dropped": "Journal dropped",
	}
	for kind, want := range cases {
		t.Run(kind, func(t *testing.T) {
			got := explainEvent(journal.Record{Kind: kind, Data: data, PID: 123, Incarnation: 3})
			if !strings.Contains(got, want) || strings.ContainsAny(got, "{}") {
				t.Fatalf("%s = %q, want %q without raw JSON", kind, got, want)
			}
		})
	}
	unknown := explainEvent(journal.Record{Kind: "future_kind", Data: map[string]any{"sample": map[string]any{"cores": 2}, "count": 3}})
	if unknown != "future_kind: count=3 sample=(cores=2)" {
		t.Fatalf("unknown kind = %q", unknown)
	}
	legacyRefusal := explainEvent(journal.Record{Kind: "handshake_refused", Data: map[string]any{"message_type": "bad"}})
	if legacyRefusal != "Connection handshake refused: unsupported message bad" {
		t.Fatalf("old refusal = %q", legacyRefusal)
	}
	unknownPeer := explainEvent(journal.Record{Kind: "connection_closed", Data: map[string]any{"role": "idle"}})
	if unknownPeer != "Connection closed for an unknown peer (idle)" {
		t.Fatalf("unknown peer = %q", unknownPeer)
	}
}

func TestExplainReattachOmitsTimeDerivedIncarnation(t *testing.T) {
	got := explainEvent(journal.Record{Kind: "reattach_accepted", Incarnation: uint64(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC).UnixNano())})
	if got != "Reattached after daemon replacement" {
		t.Fatalf("reattach explanation = %q", got)
	}
}

func TestDaemonEventsBoundsFilteredOutputAndContinues(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "wingd")
	for i := range 55 {
		if err := journal.AppendSupervisor(dir, journal.Record{Kind: "queued", RunID: "run-1", Data: map[string]any{"index": i}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 10 {
		if err := journal.AppendSupervisor(dir, journal.Record{Kind: "queued", RunID: "other-run", Data: map[string]any{"index": i}}); err != nil {
			t.Fatal(err)
		}
	}
	errReader, errWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = errWriter
	defer func() { os.Stderr = oldStderr }()
	t.Setenv("SPARKWING_HOME", home)
	first := captureDaemonOutput(t, func() error { return runDaemonEvents([]string{"--run", "run-1"}) })
	_ = errWriter.Close()
	os.Stderr = oldStderr
	note, err := io.ReadAll(errReader)
	_ = errReader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(first, "\n") != 50 || !strings.Contains(strings.Split(first, "\n")[0], `"index":5`) || !strings.Contains(first, `"index":54`) {
		t.Fatalf("first page: %s", first)
	}
	if !strings.Contains(string(note), "--offset 50") {
		t.Fatalf("continuation: %q", note)
	}
	t.Setenv("SPARKWING_HOME", home)
	older := captureDaemonOutput(t, func() error { return runDaemonEvents([]string{"--run", "run-1", "--offset", "50"}) })
	if strings.Count(older, "\n") != 5 || !strings.Contains(strings.Split(older, "\n")[0], `"index":0`) || !strings.Contains(older, `"index":4`) {
		t.Fatalf("older page: %s", older)
	}
	t.Setenv("SPARKWING_HOME", home)
	all := captureDaemonOutput(t, func() error { return runDaemonEvents([]string{"--run", "run-1", "--limit", "0"}) })
	if strings.Count(all, "\n") != 55 {
		t.Fatalf("all records: got %d", strings.Count(all, "\n"))
	}
}
