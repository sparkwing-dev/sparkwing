package journal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestJournalRotationCapAndRead(t *testing.T) {
	dir := t.TempDir()
	for i := range 20 {
		if err := appendRecord(dir, daemonFile, 180, 3, Record{TS: time.Unix(int64(i), 0), Seq: uint64(i), Kind: "request"}); err != nil {
			t.Fatal(err)
		}
	}
	var total int64
	for i := range 3 {
		path := filepath.Join(dir, daemonFile)
		if i > 0 {
			path += "." + string(rune('0'+i))
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		total += fi.Size()
	}
	if total > 3*180 {
		t.Fatalf("retained %d bytes, cap 540", total)
	}
	records, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) == 0 || records[len(records)-1].Seq != 19 {
		t.Fatalf("retained records: %v", records)
	}
}

func TestJournalOverflowCountsDroppedEventsWithoutWaitingForWriter(t *testing.T) {
	w := &Writer{ch: make(chan Record, 2), incarnation: 1}
	w.Enqueue(Record{Kind: "one"})
	w.Enqueue(Record{Kind: "two"})
	w.Enqueue(Record{Kind: "overflow"})
	if w.dropped != 1 {
		t.Fatalf("dropped = %d", w.dropped)
	}
	<-w.ch
	<-w.ch
	w.Enqueue(Record{Kind: "next"})
	dropped := <-w.ch
	if dropped.Kind != "dropped" || dropped.Data["count"] != uint64(1) {
		t.Fatalf("drop record: %+v", dropped)
	}
	if next := <-w.ch; next.Kind != "next" || next.Seq <= dropped.Seq {
		t.Fatalf("next record: %+v", next)
	}
}

func TestJournalIncarnationIncreases(t *testing.T) {
	dir := t.TempDir()
	first := NextIncarnation()
	if first == 0 {
		t.Fatal("zero incarnation")
	}
	if second := NextIncarnation(); second <= first {
		t.Fatalf("incarnation did not advance: %d, %d", first, second)
	}
	if _, err := PersistIncarnation(dir, first); err != nil {
		t.Fatal(err)
	}
	if _, err := PersistIncarnation(dir, first+1); err != nil {
		t.Fatal(err)
	}
	if Incarnation(dir) != first+1 {
		t.Fatalf("incarnation: %d", Incarnation(dir))
	}
}

func TestJournalReadIgnoresInterruptedFinalLine(t *testing.T) {
	dir := t.TempDir()
	if err := appendRecord(dir, daemonFile, 1024, 3, Record{Kind: "ready"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, daemonFile), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := appendRecord(dir, daemonFile, 1024, 3, Record{Kind: "shutdown"}); err != nil {
		t.Fatal(err)
	}
	records, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Kind != "ready" || records[1].Kind != "shutdown" {
		t.Fatalf("records: %+v", records)
	}
}

func TestJournalIncarnationRepairsCorruptFile(t *testing.T) {
	for _, content := range []string{"", "not-a-number", "3 junk", "18446744073709551614 junk"} {
		t.Run(content, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "incarnation")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			n := uint64(42)
			if _, err := PersistIncarnation(dir, n); err == nil {
				t.Fatal("missing corruption warning")
			}
			if got := Incarnation(dir); got != n {
				t.Fatalf("repaired incarnation = %d, want %d", got, n)
			}
			if _, err := PersistIncarnation(dir, n+1); err != nil || Incarnation(dir) != n+1 {
				t.Fatalf("next after repair = %d, %v", Incarnation(dir), err)
			}
		})
	}
}

func TestJournalIncarnationWriteFailureKeepsSelectedValue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if _, err := PersistIncarnation(dir, 7); err == nil {
		t.Fatal("missing write warning")
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PersistIncarnation(dir, 7); err != nil || Incarnation(dir) != 7 {
		t.Fatalf("repaired write = %d, %v", Incarnation(dir), err)
	}
}

func TestJournalIncarnationContinuesWhenReadFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "incarnation")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PersistIncarnation(dir, 42); err == nil {
		t.Fatal("missing read warning")
	}
}

func TestJournalIncarnationOlderRepairDoesNotRegressCounter(t *testing.T) {
	dir := t.TempDir()
	if _, err := PersistIncarnation(dir, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := PersistIncarnation(dir, 99); err != nil {
		t.Fatal(err)
	}
	if got := Incarnation(dir); got != 101 {
		t.Fatalf("incarnation = %d, want 101", got)
	}
}

func TestJournalIncarnationRemovesInterruptedTemps(t *testing.T) {
	dir := t.TempDir()
	if _, err := PersistIncarnation(dir, 10); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "incarnation-abandoned.tmp")
	if err := os.WriteFile(stale, []byte("11"), 0o600); err != nil {
		t.Fatal(err)
	}
	selected, err := PersistIncarnation(dir, 1)
	if err != nil || selected != 11 || Incarnation(dir) != 11 {
		t.Fatalf("selected = %d, stored = %d, err = %v", selected, Incarnation(dir), err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("interrupted temp remains: %v", err)
	}
}

func TestJournalIncarnationCounterExhaustion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "incarnation"), []byte("18446744073709551615"), 0o600); err != nil {
		t.Fatal(err)
	}
	selected, err := PersistIncarnation(dir, 1)
	if selected != 0 || err == nil || Incarnation(dir) != ^uint64(0) {
		t.Fatalf("selected = %d, stored = %d, err = %v", selected, Incarnation(dir), err)
	}
}

func TestJournalReadSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	a, _ := json.Marshal(Record{Kind: "a", TS: time.Unix(1, 0)})
	b, _ := json.Marshal(Record{Kind: "b", TS: time.Unix(2, 0)})
	body := bytes.Join([][]byte{a, []byte("{bad"), bytes.Repeat([]byte{'x'}, maxRecordBytes+1), b, []byte(`{"kind":`)}, []byte{'\n'})
	if err := os.WriteFile(filepath.Join(dir, supervisorFile), body, 0o600); err != nil {
		t.Fatal(err)
	}
	records, skipped, err := ReadWithStats(dir)
	if err != nil || skipped != 3 || len(records) != 2 || records[0].Kind != "a" || records[1].Kind != "b" {
		t.Fatalf("read = %+v, skipped %d, %v", records, skipped, err)
	}
	if old, err := Read(dir); err != nil || len(old) != len(records) {
		t.Fatalf("compatibility read = %+v, %v", old, err)
	}
}

func TestJournalReadOrdersEqualTimestampsByIncarnationAndSeq(t *testing.T) {
	dir := t.TempDir()
	ts := time.Unix(1, 0)
	for _, r := range []Record{
		{TS: ts, Incarnation: 2, Seq: 2, Kind: "last"},
		{TS: ts, Incarnation: 1, Seq: 3, Kind: "first"},
		{TS: ts, Incarnation: 2, Seq: 1, Kind: "middle"},
	} {
		if err := appendRecord(dir, daemonFile, 1024, 3, r); err != nil {
			t.Fatal(err)
		}
	}
	records, _, err := ReadWithStats(dir)
	if err != nil || len(records) != 3 || records[0].Kind != "first" || records[1].Kind != "middle" || records[2].Kind != "last" {
		t.Fatalf("ordered records = %+v, %v", records, err)
	}
}

func TestJournalSupervisorPreservesAssignedTimestamp(t *testing.T) {
	dir := t.TempDir()
	ts := time.Unix(12, 34).UTC()
	if err := AppendSupervisor(dir, Record{TS: ts, Kind: "elected"}); err != nil {
		t.Fatal(err)
	}
	records, _, err := ReadWithStats(dir)
	if err != nil || len(records) != 1 || !records[0].TS.Equal(ts) || records[0].Source != "supervisor" {
		t.Fatalf("records = %+v, %v", records, err)
	}
}

func TestJournalCloseCountsAbandonedRecordsAtDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		logged := make(chan string, 1)
		w := &Writer{ch: make(chan Record, 2), done: make(chan struct{}), incarnation: 1, logf: func(format string, args ...any) {
			logged <- fmt.Sprintf(format, args...)
		}}
		w.Enqueue(Record{Kind: "a"})
		w.Enqueue(Record{Kind: "b"})
		w.Enqueue(Record{Kind: "lost"})
		w.Close()
		if w.dropped != 3 {
			t.Fatalf("dropped = %d, want 3", w.dropped)
		}
		if !w.closed || len(w.ch) != 0 {
			t.Fatalf("close left %d records, closed %t", len(w.ch), w.closed)
		}
		synctest.Wait()
		if message := <-logged; message != "journal: close deadline dropped 3 queued records" {
			t.Fatalf("log: %s", message)
		}
	})
}

func TestJournalWriterPersistsAcrossEvents(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, 3, nil)
	for i := range 20 {
		w.Enqueue(Record{TS: time.Unix(1, 0), Kind: "event", Data: map[string]any{"n": i}})
	}
	w.Close()
	records, skipped, err := ReadWithStats(dir)
	if err != nil || skipped != 0 || len(records) != 20 {
		t.Fatalf("records = %d, skipped %d, %v", len(records), skipped, err)
	}
	for i, r := range records {
		if r.Seq != uint64(i+1) || r.Incarnation != 3 || !r.TS.After(time.Unix(1, 0)) {
			t.Fatalf("record %d: %+v", i, r)
		}
	}
}
