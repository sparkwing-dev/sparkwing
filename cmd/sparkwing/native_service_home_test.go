package main

import (
	"os/user"
	"testing"
)

func TestNativeServiceHomeIgnoresCallerHomeForLaunchd(t *testing.T) {
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	t.Setenv("HOME", private)
	for _, goos := range []string{"linux", "darwin"} {
		got, err := nativeServiceHome(goos, private)
		want := private
		if goos == "darwin" {
			want = account.HomeDir
		}
		if err != nil || got != want {
			t.Fatalf("%s home = %q, %v; want %q", goos, got, err, want)
		}
	}
}
