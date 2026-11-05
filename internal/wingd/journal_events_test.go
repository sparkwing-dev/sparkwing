package wingd

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
)

func TestParticipantJournalKeepsHolderIdentity(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d := &Daemon{cfg: Config{Now: func() time.Time { return now }}, journal: journal.NewWriter(dir, 3, nil)}
	d.recordJournal("request", &conn{journalRunID: "run-1", pipeline: "build", pid: 999}, map[string]any{"role": "idle"})
	d.journal.Close()
	records, _, err := journal.ReadWithStats(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].RunID != "run-1" || records[0].Pipeline != "build" || records[0].PID != 999 {
		t.Fatalf("holder identity = %+v", records)
	}
}
