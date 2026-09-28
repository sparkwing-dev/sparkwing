package journal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotationCapAndRead(t *testing.T) {
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

func TestOverflowCountsDroppedEventsWithoutWaitingForWriter(t *testing.T) {
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

func TestIncarnationIncreases(t *testing.T) {
	dir := t.TempDir()
	first, err := NextIncarnation(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NextIncarnation(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 2 || Incarnation(dir) != second {
		t.Fatalf("incarnations: %d %d", first, second)
	}
}

func TestReadIgnoresInterruptedFinalLine(t *testing.T) {
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
