package supervise

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
)

func TestJournalReplacementCapturesDumpAndEvidence(t *testing.T) {
	dir := t.TempDir()
	var kind string
	var evidence map[string]any
	recordReplacement(context.Background(), newSupervisorTestChild(), Deps{
		CaptureDump: func(context.Context, Child) (string, error) {
			path := filepath.Join(dir, "dump.txt")
			return path, os.WriteFile(path, []byte("goroutine 1"), 0o600)
		},
		Journal: func(k string, data map[string]any) { kind, evidence = k, data },
	}, 3, errors.New("probe timed out"), 40, 42, time.Minute, true, 4*time.Second)
	if kind != "replacement" || evidence["failed_probes"] != 3 || evidence["last_heartbeat_counter"] != uint64(42) || evidence["ceiling"] != true {
		t.Fatalf("evidence: %s %+v", kind, evidence)
	}
	path, ok := evidence["dump_path"].(string)
	if !ok {
		t.Fatalf("dump path: %+v", evidence)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "goroutine 1" {
		t.Fatalf("dump: %q %v", body, err)
	}
}

func TestJournalReplacementBoundsBlockedCapture(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var evidence map[string]any
		start := time.Now()
		recordReplacement(context.Background(), newSupervisorTestChild(), Deps{
			CaptureDump: func(context.Context, Child) (string, error) {
				<-release
				return "", nil
			},
			Journal: func(_ string, data map[string]any) { evidence = data },
		}, 3, errors.New("probe timed out"), 1, 1, time.Second, false, 0)
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("replacement capture took %s", elapsed)
		}
		if evidence["dump_error"] != "goroutine dump timed out" {
			t.Fatalf("replacement evidence: %+v", evidence)
		}
		close(release)
		synctest.Wait()
	})
}

type dumpTestChild struct {
	source string
	body   []byte
}

func (c dumpTestChild) Wait() <-chan error { return make(chan error) }
func (c dumpTestChild) Terminate() error   { return nil }
func (c dumpTestChild) Kill() error        { return nil }
func (c dumpTestChild) dumpSignal() error {
	if c.body == nil {
		c.body = []byte("goroutine 1 [running]")
	}
	if err := os.WriteFile(c.source+".tmp", c.body, 0o600); err != nil {
		return err
	}
	return os.Rename(c.source+".tmp", c.source)
}

func TestJournalCaptureDumpKeepsSeparateFile(t *testing.T) {
	dir := t.TempDir()
	path, err := captureDump(context.Background(), dumpTestChild{source: filepath.Join(dir, "d.log.stacks")}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if path == filepath.Join(dir, "d.log.stacks") {
		t.Fatal("dump overwrote signal handler output")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "goroutine 1 [running]" {
		t.Fatalf("dump: %q %v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(dir, "d.log.stacks")); err != nil || string(body) != "goroutine 1 [running]" {
		t.Fatalf("latest source: %q %v", body, err)
	}
}

func TestJournalCaptureDumpPrunesOldFiles(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "d.log.stacks")
	if err := os.WriteFile(source, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxDumps; i++ {
		path := filepath.Join(dir, fmt.Sprintf("dump-%d.txt", i))
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Unix(int64(i), 0), time.Unix(int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	path, err := captureDump(context.Background(), dumpTestChild{source: source}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("newest dump missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dump-1.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oldest dump remains: %v", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("latest source missing: %v", err)
	}
	for i := 2; i <= maxDumps; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("dump-%d.txt", i))); err != nil {
			t.Fatalf("recent dump %d missing: %v", i, err)
		}
	}
}

func TestJournalCaptureDumpKeepsNewFileAfterClockMovesBack(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= maxDumps; i++ {
		path := filepath.Join(dir, fmt.Sprintf("dump-%d.txt", int64(math.MaxInt64-i)))
		if err := os.WriteFile(path, []byte("future"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := captureDump(context.Background(), dumpTestChild{source: filepath.Join(dir, "d.log.stacks")}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("new dump was pruned: %v", err)
	}
}

func TestJournalCaptureDumpCapsBytes(t *testing.T) {
	dir := t.TempDir()
	path, err := captureDump(context.Background(), dumpTestChild{
		source: filepath.Join(dir, "d.log.stacks"),
		body:   []byte(strings.Repeat("x", maxDumpBytes+1)),
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != maxDumpBytes {
		t.Fatalf("dump size: %d", info.Size())
	}
}

type failedDumpChild struct{ err error }

func (c failedDumpChild) Wait() <-chan error { return nil }
func (c failedDumpChild) Terminate() error   { return nil }
func (c failedDumpChild) Kill() error        { return nil }
func (c failedDumpChild) dumpSignal() error  { return c.err }

type blockingSignalChild struct {
	failedDumpChild
	release <-chan struct{}
}

func (c blockingSignalChild) dumpSignal() error {
	<-c.release
	return nil
}

func TestJournalCaptureDumpPrunesOnFailure(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "d.log.stacks")
	if err := os.WriteFile(source, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= maxDumps+1; i++ {
		path := filepath.Join(dir, fmt.Sprintf("dump-%d.txt", i))
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Unix(int64(i), 0), time.Unix(int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	oversized := filepath.Join(dir, "dump-100.txt")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", maxDumpBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := captureDump(context.Background(), failedDumpChild{err: errors.New("signal failed")}, dir); err == nil {
		t.Fatal("expected signal failure")
	}
	if body, err := os.ReadFile(source); err != nil || string(body) != "stale" {
		t.Fatalf("previous source: %q %v", body, err)
	}
	for _, path := range []string{filepath.Join(dir, "dump-1.txt"), oversized} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old dump remains at %s: %v", path, err)
		}
	}
}

func TestJournalCaptureDumpBoundsWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := captureDump(ctx, failedDumpChild{}, t.TempDir())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("capture: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 1100*time.Millisecond {
			t.Fatalf("capture took %s", elapsed)
		}
	})
}

func TestJournalLateCaptureLeavesSuccessorSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		source := filepath.Join(dir, "d.log.stacks")
		if err := os.WriteFile(source, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		release := make(chan struct{})
		recordReplacement(context.Background(), blockingSignalChild{release: release}, Deps{
			CaptureDump: func(ctx context.Context, child Child) (string, error) {
				return captureDump(ctx, child, dir)
			},
		}, 1, errors.New("probe failed"), 0, 0, 0, false, 0)
		if err := os.WriteFile(source+".tmp", []byte("successor"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(source+".tmp", source); err != nil {
			t.Fatal(err)
		}
		close(release)
		synctest.Wait()
		body, err := os.ReadFile(source)
		if err != nil || string(body) != "successor" {
			t.Fatalf("successor source: %q %v", body, err)
		}
		if paths, err := filepath.Glob(filepath.Join(dir, "dump-*.txt")); err != nil || len(paths) != 0 {
			t.Fatalf("late dump paths: %v %v", paths, err)
		}
	})
}

func TestJournalLateDumpWriteRemovesOnlyItsOwnFile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		started := make(chan string, 1)
		release := make(chan struct{})
		recordReplacement(context.Background(), dumpTestChild{source: filepath.Join(dir, "d.log.stacks")}, Deps{
			CaptureDump: func(ctx context.Context, child Child) (string, error) {
				return captureDumpWithWrite(ctx, child, dir, func(path string, body []byte) error {
					started <- path
					<-release
					return os.WriteFile(path, body, 0o600)
				})
			},
		}, 1, errors.New("probe failed"), 0, 0, 0, false, 0)
		oldPath := <-started
		successorPath := filepath.Join(dir, fmt.Sprintf("dump-%d.txt", int64(math.MaxInt64)))
		if err := os.WriteFile(successorPath, []byte("successor"), 0o600); err != nil {
			t.Fatal(err)
		}
		close(release)
		synctest.Wait()
		if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("late dump remains: %v", err)
		}
		if body, err := os.ReadFile(successorPath); err != nil || string(body) != "successor" {
			t.Fatalf("successor dump: %q %v", body, err)
		}
	})
}

func TestJournalFailedDumpWriteRemovesPartialFile(t *testing.T) {
	dir := t.TempDir()
	var partial string
	_, err := captureDumpWithWrite(context.Background(), dumpTestChild{source: filepath.Join(dir, "d.log.stacks")}, dir, func(path string, body []byte) error {
		partial = path
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return err
		}
		return errors.New("write failed")
	})
	if err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("capture: %v", err)
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial dump remains: %v", err)
	}
}

func TestJournalSupervisorQueueDoesNotBlockOnDisk(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		release := make(chan struct{})
		var records []journal.Record
		queue := queueSupervisorJournal(ctx, func(r journal.Record) error {
			if len(records) == 0 {
				<-release
			}
			records = append(records, r)
			return nil
		}, nil)
		queue("first", nil)
		synctest.Wait()
		done := make(chan struct{})
		go func() {
			for i := 0; i <= supervisorJournalBuffer; i++ {
				queue("later", nil)
			}
			close(done)
		}()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("journal queue blocked behind disk append")
		}
		close(release)
		synctest.Wait()
		if len(records) != supervisorJournalBuffer+2 || records[1].Kind != "dropped" || records[1].Data["count"] != uint64(1) {
			t.Fatalf("records: %+v", records)
		}
		for i, r := range records {
			if r.Seq != uint64(i+1) || r.TS.IsZero() {
				t.Fatalf("record %d: %+v", i, r)
			}
		}
		cancel()
		synctest.Wait()
	})
}
