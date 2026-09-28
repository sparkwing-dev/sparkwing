package supervise

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplacementCapturesDumpAndEvidence(t *testing.T) {
	dir := t.TempDir()
	var kind string
	var evidence map[string]any
	recordReplacement(newSupervisorTestChild(), Deps{
		CaptureDump: func(Child) (string, error) {
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

type dumpTestChild struct{ source string }

func (c dumpTestChild) Wait() <-chan error { return make(chan error) }
func (c dumpTestChild) Terminate() error   { return nil }
func (c dumpTestChild) Kill() error        { return nil }
func (c dumpTestChild) dumpSignal() error {
	return os.WriteFile(c.source, []byte("goroutine 1 [running]"), 0o600)
}

func TestCaptureDumpKeepsSeparateFile(t *testing.T) {
	dir := t.TempDir()
	path, err := captureDump(dumpTestChild{source: filepath.Join(dir, "d.log.stacks")}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if path == filepath.Join(dir, "d.log.stacks") {
		t.Fatal("dump overwrote signal handler output")
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "goroutine 1 [running]" {
		t.Fatalf("dump: %q %v", body, err)
	}
}
