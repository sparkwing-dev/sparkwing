//go:build !windows

package fssecure_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestRepairTreeTightensDataAndPreservesPrivateExecutables(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(filepath.Join(root, "runs", "run-1"), 0o777); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, filepath.Join(root, "runs"), filepath.Join(root, "runs", "run-1")} {
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	data := filepath.Join(root, "runs", "run-1", "node.log")
	if err := os.WriteFile(data, []byte("secret"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0o666); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "cache", "pipelines", "v1", "entries", "key", "pipelines")
	if err := os.MkdirAll(filepath.Dir(bin), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatal(err)
	}

	changes, err := fssecure.RepairTree(root, fileIdentity(t, root), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) < 5 {
		t.Fatalf("changes = %+v, want private directories, data, and cached executable modes normalized", changes)
	}
	assertPerm(t, root, fssecure.DirMode)
	assertPerm(t, filepath.Join(root, "runs"), fssecure.DirMode)
	assertPerm(t, filepath.Join(root, "runs", "run-1"), fssecure.DirMode)
	assertPerm(t, data, fssecure.FileMode)
	assertPerm(t, bin, fssecure.DirMode)
	assertPerm(t, outside, 0o666)
	body, err := os.ReadFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "secret" {
		t.Fatalf("data changed during permission repair: %q", body)
	}
}

func TestRepairTreeStripsStrayExecuteBitsFromData(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.db", "runs.log"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("data"), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fssecure.RepairTree(root, fileIdentity(t, root), false); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, filepath.Join(root, "state.db"), fssecure.FileMode)
	assertPerm(t, filepath.Join(root, "runs.log"), fssecure.FileMode)
}

func TestEnsureDirDoesNotBroadenExistingOwnerAccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(root, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := fssecure.EnsureDir(root); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, root, 0o500)
}

func TestRepairTreeDoesNotBroadenExistingOwnerAccess(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	dir := filepath.Join(root, "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "state.db")
	executable := filepath.Join(root, "node-runner", "runner")
	if err := os.WriteFile(data, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{
		root:       0o500,
		dir:        0o500,
		data:       0o400,
		executable: 0o500,
	} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { _ = os.Chmod(root, 0o700) }()

	if _, err := fssecure.RepairTree(root, fileIdentity(t, root), false); err != nil {
		t.Fatal(err)
	}
	assertPerm(t, root, 0o500)
	assertPerm(t, dir, 0o500)
	assertPerm(t, data, 0o400)
	assertPerm(t, executable, 0o500)
}

func TestRepairTreeRefusesRootReplacedAfterRecognition(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "home")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	recognized, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(parent, "recognized-home")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := fssecure.RepairTree(root, recognized, false); err == nil {
		t.Fatal("RepairTree accepted a replacement root")
	}
	assertPerm(t, root, 0o755)
}

func TestPrivateCreationIgnoresPermissiveUmask(t *testing.T) {
	if os.Getenv("SPARKWING_FSSECURE_UMASK_HELPER") == "1" {
		syscall.Umask(0)
		root := os.Getenv("SPARKWING_FSSECURE_TEST_ROOT")
		if err := os.MkdirAll(root, 0o777); err != nil {
			t.Fatal(err)
		}
		if err := fssecure.EnsureDir(root); err != nil {
			t.Fatal(err)
		}
		opened := filepath.Join(root, "opened")
		f, err := fssecure.OpenFile(opened, os.O_CREATE|os.O_WRONLY)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		written := filepath.Join(root, "written")
		if err := os.WriteFile(written, []byte("legacy"), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := fssecure.WriteFile(written, []byte("private")); err != nil {
			t.Fatal(err)
		}
		assertPerm(t, root, fssecure.DirMode)
		assertPerm(t, opened, fssecure.FileMode)
		assertPerm(t, written, fssecure.FileMode)
		return
	}

	root := filepath.Join(t.TempDir(), "private")
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrivateCreationIgnoresPermissiveUmask$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_FSSECURE_UMASK_HELPER=1",
		"SPARKWING_FSSECURE_TEST_ROOT="+root,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("umask helper: %v\n%s", err, out)
	}
}

func TestRepairTreeRefusesUnsafeRoots(t *testing.T) {
	for _, root := range []string{"", ".", string(filepath.Separator)} {
		if _, err := fssecure.RepairTree(root, nil, false); err == nil {
			t.Fatalf("RepairTree(%q) succeeded", root)
		}
	}
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	if _, err := fssecure.RepairTree(userHome, fileIdentity(t, userHome), true); err == nil {
		t.Fatalf("RepairTree(%q) accepted the entire user home", userHome)
	}
}

func TestRepairTreeRefusesEffectiveHomeThroughSymlinks(t *testing.T) {
	base := t.TempDir()
	targetParent := filepath.Join(base, "target")
	userHome := filepath.Join(targetParent, "userhome")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	if _, err := fssecure.RepairTree(targetParent, fileIdentity(t, targetParent), true); err == nil {
		t.Fatal("RepairTree accepted an ancestor of the effective user home")
	}

	alias := filepath.Join(base, "alias")
	if err := os.Symlink(targetParent, alias); err != nil {
		t.Fatal(err)
	}
	viaIntermediate := filepath.Join(alias, "userhome")
	if _, err := fssecure.RepairTree(viaIntermediate, nil, true); err == nil {
		t.Fatal("RepairTree accepted the effective user home through a symlinked ancestor")
	}
	if _, err := fssecure.RepairTree(alias, nil, true); err == nil {
		t.Fatal("RepairTree accepted a symlink root")
	}
}

func TestRepairTreeDryRunReportsWithoutChanging(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	changes, err := fssecure.RepairTree(root, fileIdentity(t, root), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Before != "0755" || changes[0].After != "0700" {
		t.Fatalf("changes = %+v", changes)
	}
	assertPerm(t, root, 0o755)
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}

func fileIdentity(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
