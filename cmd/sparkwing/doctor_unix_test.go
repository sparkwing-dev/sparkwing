//go:build !windows

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/installsite"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func holdFlock(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock %s: %v", path, err)
	}
	t.Cleanup(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() })
}

func TestDiagnose_ReportsLiveLegacyHolderWithoutDeleting(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	boxDir := p.BoxSlotDir()
	if err := os.MkdirAll(boxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staleName := "holder-pid99999-1700000000000000000-1.lock"
	if err := os.WriteFile(filepath.Join(boxDir, staleName), []byte("pid=99999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	liveName := "holder-pid4242-1800000000000000000-1.lock"
	holdFlock(t, filepath.Join(boxDir, liveName), "pid=4242\nrun=run-legacy\n")

	rep, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if len(rep.LiveLegacyHolders) != 1 || rep.LiveLegacyHolders[0].PID != 4242 {
		t.Fatalf("LiveLegacyHolders = %+v, want one row for pid 4242", rep.LiveLegacyHolders)
	}
	if rep.LegacyBoxSlotFilesRemoved != 0 {
		t.Fatalf("removed %d files while a holder is live, want 0", rep.LegacyBoxSlotFilesRemoved)
	}
	if _, err := os.Stat(filepath.Join(boxDir, liveName)); err != nil {
		t.Fatalf("live holder marker removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(boxDir, staleName)); err != nil {
		t.Fatalf("stale marker removed while a holder is live: %v", err)
	}
}

func TestDiagnose_PurgesIdleLegacyBoxSlots(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	boxDir := p.BoxSlotDir()
	if err := os.MkdirAll(boxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"holder-pid99999-1700000000000000000-1.lock", "coord.lock", "cap.control"} {
		if err := os.WriteFile(filepath.Join(boxDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if rep.LegacyBoxSlotFilesRemoved != 2 {
		t.Fatalf("LegacyBoxSlotFilesRemoved = %d, want 2", rep.LegacyBoxSlotFilesRemoved)
	}
	if _, err := os.Stat(filepath.Join(boxDir, "coord.lock")); err != nil {
		t.Fatalf("coordination lock did not survive purge: %v", err)
	}
}

func TestDiagnose_ReportsAndRepairsPermissiveLegacyHome(t *testing.T) {
	p := doctorHome(t)
	ctx := context.Background()
	withStore(t, p, func(st *store.Store) {
		if err := st.CreateRun(ctx, store.Run{
			ID: "run-known", Pipeline: "demo", Status: "success", StartedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	})
	runDir := p.RunDir("run-known")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := p.NodeLog("run-known", "build")
	if err := os.WriteFile(logPath, []byte("sensitive output"), 0o600); err != nil {
		t.Fatal(err)
	}
	strayExecutable := filepath.Join(p.Root, "pipeline-bin")
	if err := os.WriteFile(strayExecutable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(p.Root, "outside-link")); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{
		p.Root:          0o755,
		p.RunsDir():     0o777,
		runDir:          0o755,
		p.StateDB():     0o666,
		logPath:         0o664,
		strayExecutable: 0o755,
		outside:         0o666,
	} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod %s: %v", path, err)
		}
	}

	beforeDry := snapshotDoctorTree(t, p.Root)
	dry, err := diagnose(ctx, p, p.Root, true)
	if err != nil {
		t.Fatalf("dry-run diagnose: %v", err)
	}
	if len(dry.PermissionRepairs) == 0 {
		t.Fatal("dry-run diagnose did not report permissive paths")
	}
	assertMode(t, p.Root, 0o755)
	assertMode(t, p.StateDB(), 0o666)
	assertMode(t, logPath, 0o664)
	if afterDry := snapshotDoctorTree(t, p.Root); !reflect.DeepEqual(afterDry, beforeDry) {
		t.Fatalf("dry-run changed home\nbefore: %+v\nafter:  %+v", beforeDry, afterDry)
	}

	repaired, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("repair diagnose: %v", err)
	}
	if len(repaired.PermissionRepairs) == 0 {
		t.Fatal("repair diagnose did not name tightened paths")
	}
	assertMode(t, p.Root, 0o700)
	assertMode(t, p.RunsDir(), 0o700)
	assertMode(t, runDir, 0o700)
	assertMode(t, p.StateDB(), 0o600)
	assertMode(t, logPath, 0o600)
	assertMode(t, strayExecutable, 0o600)
	assertMode(t, outside, 0o666)
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("known run damaged during permission repair: %v", err)
	}

	second, err := diagnose(ctx, p, p.Root, false)
	if err != nil {
		t.Fatalf("second diagnose: %v", err)
	}
	if len(second.PermissionRepairs) != 0 {
		t.Fatalf("second diagnose repeated permission repairs: %+v", second.PermissionRepairs)
	}
}

func TestDiagnose_DryRunDoesNotCreateStateInEmptyHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	p := paths.PathsAt(root)
	before := snapshotDoctorTree(t, root)
	report, err := diagnose(context.Background(), p, root, true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun {
		t.Fatal("dry-run report did not carry dry_run=true")
	}
	if after := snapshotDoctorTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("empty-home dry-run changed home\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestDiagnose_RefusesUnrecognizedDirectoryWithoutChangingIt(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "release.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	before := snapshotDoctorTree(t, root)
	_, err := diagnose(context.Background(), paths.PathsAt(root), root, false)
	if err == nil || !strings.Contains(err.Error(), "unrecognized sparkwing home") {
		t.Fatalf("diagnose error = %v, want safe refusal", err)
	}
	if after := snapshotDoctorTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("refused directory changed\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestDiagnose_RefusesWeakSparkwingNamedMarkers(t *testing.T) {
	for _, seed := range []func(string) error{
		func(root string) error {
			return os.MkdirAll(filepath.Join(root, "runs", "run-looking"), 0o755)
		},
		func(root string) error {
			dir := filepath.Join(root, "last-version.d")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "install"), []byte("v0.36.0\n"), 0o644)
		},
		func(root string) error {
			dir := filepath.Join(root, "wingd")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"schema":1}`), 0o644)
		},
	} {
		root := filepath.Join(t.TempDir(), "home")
		if err := seed(root); err != nil {
			t.Fatal(err)
		}
		before := snapshotDoctorTree(t, root)
		_, err := diagnose(context.Background(), paths.PathsAt(root), root, false)
		if err == nil || !strings.Contains(err.Error(), "unrecognized sparkwing home") {
			t.Fatalf("diagnose error = %v, want safe refusal", err)
		}
		if after := snapshotDoctorTree(t, root); !reflect.DeepEqual(after, before) {
			t.Fatalf("refused marker changed\nbefore: %+v\nafter:  %+v", before, after)
		}
	}
}

func TestDiagnose_RefusesMismatchedHomeIdentity(t *testing.T) {
	p := doctorHome(t)
	withStore(t, p, func(_ *store.Store) {})
	other := filepath.Join(t.TempDir(), "other-home")
	before := snapshotDoctorTree(t, p.Root)
	_, err := diagnose(context.Background(), p, other, false)
	if err == nil || !strings.Contains(err.Error(), "does not identify requested home") {
		t.Fatalf("diagnose error = %v, want root mismatch refusal", err)
	}
	if after := snapshotDoctorTree(t, p.Root); !reflect.DeepEqual(after, before) {
		t.Fatalf("root mismatch changed home\nbefore: %+v\nafter:  %+v", before, after)
	}
}

func TestDiagnose_FreshMissingHomeDoesNotCreateState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-home")
	report, err := diagnose(context.Background(), paths.PathsAt(root), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Clean() {
		t.Fatalf("fresh home report = %+v, want clean", report)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh doctor created home: %v", err)
	}
}

func TestDiagnose_RecognizesVersionStampOnlyHomeWithoutCreatingState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	stampDir := filepath.Join(root, "last-version.d")
	if err := os.MkdirAll(stampDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(stampDir, installsite.PathKey("/usr/local/bin/sparkwing"))
	if err := os.WriteFile(stamp, []byte("# /usr/local/bin/sparkwing\nv0.36.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := diagnose(context.Background(), paths.PathsAt(root), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.PermissionRepairs) == 0 {
		t.Fatal("version-stamp-only home did not receive permission repair")
	}
	assertMode(t, root, 0o700)
	assertMode(t, stampDir, 0o700)
	assertMode(t, stamp, 0o600)
	if _, err := os.Stat(filepath.Join(root, "state.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("doctor created state database: %v", err)
	}
}

func TestDiagnose_RecognizesSupportedWingdStateOnlyHome(t *testing.T) {
	for _, schema := range []int{1, 2} {
		root := filepath.Join(t.TempDir(), "home")
		wingdDir := filepath.Join(root, "wingd")
		if err := os.MkdirAll(wingdDir, 0o755); err != nil {
			t.Fatal(err)
		}
		state := filepath.Join(wingdDir, "state.json")
		if err := os.WriteFile(state, []byte(fmt.Sprintf(`{"schema":%d,"snapshot":{}}`, schema)), 0o644); err != nil {
			t.Fatal(err)
		}
		report, err := diagnose(context.Background(), paths.PathsAt(root), root, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.PermissionRepairs) == 0 {
			t.Fatalf("schema %d wingd home did not receive permission repair", schema)
		}
		assertMode(t, root, 0o700)
		assertMode(t, wingdDir, 0o700)
		assertMode(t, state, 0o600)
	}
}

func TestDiagnose_RefusesKnownPathSymlinksWithoutTouchingTargets(t *testing.T) {
	for _, name := range []string{"runs", "box-slots", "state.db"} {
		t.Run(name, func(t *testing.T) {
			p := doctorHome(t)
			withStore(t, p, func(_ *store.Store) {})
			targetRoot := t.TempDir()
			var link, target string
			switch name {
			case "runs":
				link = p.RunsDir()
				target = filepath.Join(targetRoot, "runs")
				if err := os.MkdirAll(filepath.Join(target, "run-outside"), 0o777); err != nil {
					t.Fatal(err)
				}
			case "box-slots":
				link = p.BoxSlotDir()
				target = filepath.Join(targetRoot, "box-slots")
				if err := os.MkdirAll(target, 0o777); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target, "victim.lock"), []byte("outside"), 0o666); err != nil {
					t.Fatal(err)
				}
			case "state.db":
				link = p.StateDB()
				target = filepath.Join(targetRoot, "state.db")
				if err := os.Rename(p.StateDB(), target); err != nil {
					t.Fatal(err)
				}
			}
			if name != "state.db" {
				if err := os.RemoveAll(link); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			before := snapshotDoctorTree(t, targetRoot)
			_, err := diagnose(context.Background(), p, p.Root, false)
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("diagnose error = %v, want symlink refusal", err)
			}
			if after := snapshotDoctorTree(t, targetRoot); !reflect.DeepEqual(after, before) {
				t.Fatalf("outside target changed\nbefore: %+v\nafter:  %+v", before, after)
			}
		})
	}
}

func TestDiagnose_RefusesSymlinkHomeWithoutTouchingTarget(t *testing.T) {
	target := doctorHome(t)
	withStore(t, target, func(_ *store.Store) {})
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(target.Root, link); err != nil {
		t.Fatal(err)
	}
	before := snapshotDoctorTree(t, target.Root)
	_, err := diagnose(context.Background(), paths.PathsAt(link), link, false)
	if err == nil || !strings.Contains(err.Error(), "symlink root") {
		t.Fatalf("diagnose error = %v, want symlink-root refusal", err)
	}
	if after := snapshotDoctorTree(t, target.Root); !reflect.DeepEqual(after, before) {
		t.Fatalf("symlink target changed\nbefore: %+v\nafter:  %+v", before, after)
	}
}

type doctorTreeEntry struct {
	Mode   os.FileMode
	Digest [sha256.Size]byte
	Target string
}

func snapshotDoctorTree(t *testing.T, root string) map[string]doctorTreeEntry {
	t.Helper()
	entries := map[string]doctorTreeEntry{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		state := doctorTreeEntry{Mode: info.Mode()}
		switch {
		case info.Mode().IsRegular():
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state.Digest = sha256.Sum256(body)
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			state.Target = target
		}
		entries[rel] = state
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
