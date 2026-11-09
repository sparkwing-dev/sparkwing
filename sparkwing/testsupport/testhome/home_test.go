package testhome

import (
	"os"
	"runtime"
	"testing"
)

func TestSetRedirectsNativeHomeAndRestoresEnvironment(t *testing.T) {
	beforeHome := os.Getenv("HOME")
	beforeProfile := os.Getenv("USERPROFILE")
	home := t.TempDir()
	t.Run("isolated", func(t *testing.T) {
		Set(t, home)
		got, err := os.UserHomeDir()
		if err != nil || got != home {
			t.Fatalf("native home = %q, %v; want %q", got, err, home)
		}
		if runtime.GOOS != "windows" && os.Getenv("USERPROFILE") != beforeProfile {
			t.Fatal("Unix USERPROFILE changed")
		}
	})
	if os.Getenv("HOME") != beforeHome || os.Getenv("USERPROFILE") != beforeProfile {
		t.Fatal("home environment was not restored")
	}
}
