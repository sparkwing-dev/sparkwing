//go:build windows

package store

import "testing"

func TestWindowsFileURLHasTheSameIdentityAsNativeDrivePath(t *testing.T) {
	native := `C:\work\private\repo.git`
	got := RepoIdentityFromURL("file:///C:/work/private/repo.git")
	if want := RepoIdentityFromPath(native); got != want {
		t.Fatalf("file URI identity = %q; want %q", got, want)
	}
}
