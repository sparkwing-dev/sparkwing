package bincache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withKeyRoots points the key directory at roots, each on a tmpfs only when
// tmpfs says so, for the length of the test.
func withKeyRoots(t *testing.T, tmpfs bool, roots ...string) {
	t.Helper()
	prevRoots, prevTmpfs := keyRootCandidates, onTmpfs
	keyRootCandidates = func() []string { return roots }
	onTmpfs = func(string) bool { return tmpfs }
	t.Cleanup(func() { keyRootCandidates, onTmpfs = prevRoots, prevTmpfs })
}

// A cloud runner writes a released deploy key only to a tmpfs: with none it
// fails the fetch, naming why, before ssh ever runs. A runner allowed a disk
// still fetches, and with a tmpfs the cloud runner does too.
func TestSSHCredentialNeedsATmpfsInCloudMode(t *testing.T) {
	repos := t.TempDir()
	_, tip := makeBareRepoWithSparkwing(t, repos, "widgets", "main")
	record := sshRecorder(t, repos, false)
	cred := DirectCredential{Kind: CredentialSSH, Host: "git.example.invalid", Secret: testDeployKey(t), KnownHosts: testKnownHosts}
	checkout := func(tmpfsOnly bool) error {
		return directCheckout(context.Background(), t.TempDir(), "ssh://git@git.example.invalid/widgets.git",
			"main", tip, filepath.Join(t.TempDir(), "run"), directOptions{protocols: "ssh", cred: cred, keyTmpfsOnly: tmpfsOnly})
	}

	disk := t.TempDir()
	withKeyRoots(t, false, disk)
	err := checkout(true)
	if err == nil || !strings.Contains(err.Error(), "tmpfs") {
		t.Fatalf("cloud mode without a tmpfs = %v, want a refusal naming the tmpfs", err)
	}
	if _, err := os.Stat(filepath.Join(record, "keypath")); !os.IsNotExist(err) {
		t.Fatalf("ssh ran with a key written to disk: %v", err)
	}
	if entries, _ := os.ReadDir(disk); len(entries) != 0 {
		t.Fatalf("the refused fetch left %d entries on disk", len(entries))
	}
	if err := checkout(false); err != nil {
		t.Fatalf("control: a runner allowed a disk = %v", err)
	}

	mem := t.TempDir()
	withKeyRoots(t, true, mem)
	if err := checkout(true); err != nil {
		t.Fatalf("cloud mode with a tmpfs = %v", err)
	}
	if got := readRecord(t, record, "keypath"); !strings.HasPrefix(got, mem+string(os.PathSeparator)) {
		t.Fatalf("the key was written to %s, want under the tmpfs %s", got, mem)
	}
}

// At start a runner removes the key directories a crashed run of its own
// user left behind, and leaves one a live fetch still holds, anything not
// named as a key directory, and a symlink.
func TestSweepSSHKeyDirsRemovesOnlyLeftovers(t *testing.T) {
	root := t.TempDir()
	withKeyRoots(t, true, root)
	cred := DirectCredential{Kind: CredentialSSH, Host: "git.example.invalid", Secret: testDeployKey(t), KnownHosts: testKnownHosts}

	// A crash drops the lock with the process; the directory and key stay.
	crashed := filepath.Join(root, "sparkwing-git-crashed")
	if err := os.Mkdir(crashed, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{keyDirLockName, "key"} {
		if err := os.WriteFile(filepath.Join(crashed, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unlocked := filepath.Join(root, "sparkwing-git-unlocked")
	if err := os.Mkdir(unlocked, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(unlocked, old, old); err != nil {
		t.Fatal(err)
	}
	live, _, releaseLive, err := writeSSHCredential(cred, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = releaseLive() }()
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	link := filepath.Join(root, "sparkwing-git-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	n, err := SweepSSHKeyDirs()
	if err != nil || n != 2 {
		t.Fatalf("sweep = %d, %v; want the crashed and the unlocked directory", n, err)
	}
	for _, gone := range []string{crashed, unlocked} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep: %v", gone, err)
		}
	}
	for _, kept := range []string{live, other, link, target} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("the sweep removed %s: %v", kept, err)
		}
	}
}
