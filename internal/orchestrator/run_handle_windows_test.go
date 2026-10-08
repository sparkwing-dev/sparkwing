//go:build windows

package orchestrator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishRunHandleOnWindows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")
	want := NewRunHandle("run-123", "windows-verify", "logs", "running")
	if err := PublishRunHandle(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got RunHandle
	if err := json.Unmarshal(data, &got); err != nil || got != want {
		t.Fatalf("published handle = %+v, %v; want %+v", got, err, want)
	}
	if err := PublishRunHandle(path, NewRunHandle("run-456", "other", "", "running")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second publication error = %v, want existing destination", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil || got != want {
		t.Fatalf("second publication changed handle to %+v, %v", got, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".sparkwing-run-handle-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary publication files = %v, %v", leftovers, err)
	}
	if err := PublishRunHandle(filepath.Join(dir, "missing", "run.json"), want); err == nil {
		t.Fatal("missing destination directory was accepted")
	}
}
