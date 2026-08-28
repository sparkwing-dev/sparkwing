//go:build !windows

package opsview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestDiagnoseDanglingRunDirsStaysWithRenamedDirectory(t *testing.T) {
	home := t.TempDir()
	runsPath := filepath.Join(home, "runs")
	inside := filepath.Join(runsPath, "run-inside")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	runsRoot, err := os.OpenRoot(runsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer runsRoot.Close()

	renamed := filepath.Join(home, "runs-original")
	if err := os.Rename(runsPath, renamed); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "run-outside")
	if err := os.Mkdir(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, runsPath); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	report := DoctorReport{}
	if err := diagnoseDanglingRunDirs(context.Background(), st, runsRoot, false, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.DanglingRunDirs) != 1 || report.DanglingRunDirs[0] != "run-inside" {
		t.Fatalf("dangling run dirs = %v", report.DanglingRunDirs)
	}
	if _, err := os.Stat(filepath.Join(renamed, "run-inside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pinned run directory remains: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(victim, "keep"))
	if err != nil || string(body) != "outside" {
		t.Fatalf("outside run changed: body=%q err=%v", body, err)
	}
}

func TestOpenDoctorStateRefusesReplacedHomeBeforeStoreOpen(t *testing.T) {
	home := t.TempDir()
	p := paths.PathsAt(home)
	st, err := store.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	identity, err := os.Lstat(home)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	renamed := home + "-original"
	if err := os.Rename(home, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := paths.PathsAt(home)
	replacementStore, err := store.Open(replacement.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if err := replacementStore.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(replacement.StateDB())
	if err != nil {
		t.Fatal(err)
	}

	opened, stateFile, err := openDoctorState(p, root, identity, false)
	if opened != nil {
		_ = opened.Close()
	}
	if stateFile != nil {
		_ = stateFile.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "home root") {
		t.Fatalf("open doctor state error = %v, want replaced-root refusal", err)
	}
	after, err := os.Stat(replacement.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Fatalf("replacement state changed: before=%+v after=%+v", before, after)
	}
}
