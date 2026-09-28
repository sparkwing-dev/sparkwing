package wingd

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
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
