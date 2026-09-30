package wingd

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestConnectionJournalKeepsIdentityAfterHandoff(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d := &Daemon{cfg: Config{Now: func() time.Time { return now }}, journal: journal.NewWriter(dir, 3, nil)}
	d.recordJournal("connection_closed", &conn{id: 7, journalRunID: "run-1", pipeline: "build", pid: 999, peerPID: 123}, map[string]any{"role": "idle"})
	d.journal.Close()
	records, err := journal.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].RunID != "run-1" || records[0].Pipeline != "build" || records[0].PID != 123 || records[0].Data["connection_id"] != float64(7) {
		t.Fatalf("closed connection identity = %+v", records)
	}
}

func TestJournalRequestDoesNotAssignConnectionIdentityBeforeAdmission(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	d := configuredHandlerDaemon(t, Config{Runs: &FuncRunStore{IsTerminal: func(string) (bool, error) {
		close(entered)
		<-release
		return true, nil
	}}}, 2)
	dir := t.TempDir()
	d.journal = journal.NewWriter(dir, 1, nil)
	c, peer := handlerConn(t, d)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.handleAdmission(c, &wingwire.AdmissionRequest{RunID: "named-run", DisplayRunID: "display-run", Pipeline: "pipeline", Repo: "repo", PID: 42})
	}()
	<-entered
	d.mu.Lock()
	if c.runID != "" || c.journalRunID != "" || c.displayRunID != "" || c.pipeline != "" || c.repo != "" || c.pid != 0 {
		t.Errorf("connection identity changed before admission: %+v", c)
	}
	d.mu.Unlock()
	close(release)
	msg, err := peer.readMessage()
	if err != nil {
		t.Fatal(err)
	}
	<-done
	if evicted, ok := msg.(*wingwire.Evicted); !ok || evicted.RunID != "named-run" {
		t.Fatalf("terminal response = %#v", msg)
	}
	d.journal.Close()
	records, err := journal.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 || records[0].Kind != "request" || records[0].RunID != "named-run" || records[0].DisplayRunID != "display-run" || records[0].Pipeline != "pipeline" || records[0].Repo != "repo" || records[0].PID != 42 {
		t.Fatalf("request journal identity = %+v", records)
	}
}

func TestJournalReattachRefusalPreservesEmptyRunID(t *testing.T) {
	d, _ := restartedDaemonHoldingNestedLease(t)
	dir := t.TempDir()
	d.journal = journal.NewWriter(dir, 1, nil)
	c, peer := handlerConn(t, d)
	msg := callAndRead(t, peer, func() {
		d.handleReattach(c, &wingwire.Reattach{LeaseToken: "missing", RunID: "named-run"})
	})
	if evicted, ok := msg.(*wingwire.Evicted); !ok || evicted.RunID != "" || c.runID != "" || c.journalRunID != "" {
		t.Fatalf("refused reattach = %#v, connection run ID = %q", msg, c.runID)
	}
	d.journal.Close()
	records, err := journal.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Kind != "reattach_request" || records[0].RunID != "named-run" || records[1].Kind != "reattach_refused" || records[1].RunID != "named-run" {
		t.Fatalf("reattach journal = %+v", records)
	}
}

func TestJournalPolicyRedactsEndpointCredentials(t *testing.T) {
	policy := AdmissionPolicy{Jev: JevPolicy{Endpoint: "https://person:password@example.test/advise?token=secret&mode=fast"}}
	jev := journalPolicy(policy)["jev"].(map[string]any)
	if got := jev["endpoint"]; got != "https://example.test/advise" {
		t.Fatalf("journal endpoint = %q", got)
	}
	policy.Jev.Endpoint = "https://person:password@[bad-host/advise?token=secret"
	jev = journalPolicy(policy)["jev"].(map[string]any)
	if got := jev["endpoint"]; got != "" {
		t.Fatalf("malformed journal endpoint = %q", got)
	}
}

func TestJournalEndpointKeepsOnlyLocation(t *testing.T) {
	for raw, want := range map[string]string{
		"https://person:password@example.test/advise?token=secret#fragment": "https://example.test/advise",
		"//person:password@example.test/advise?token=secret#fragment":       "//example.test/advise",
		"person@example.test/advise?token=secret#fragment":                  "//example.test/advise",
		"person:password@example.test/advise?token=secret#fragment":         "",
		"/advise?token=secret#fragment":                                     "/advise",
		"mailto:person:password@example.test?token=secret#fragment":         "",
	} {
		if got := journalEndpoint(raw); got != want {
			t.Errorf("journalEndpoint(%q) = %q, want %q", raw, got, want)
		}
	}
}
