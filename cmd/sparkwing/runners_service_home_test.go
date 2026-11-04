package main

import (
	"path/filepath"
	"testing"
)

func TestRunnerServiceHostUsesTheResolvedStateHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "srv")
	t.Setenv("SPARKWING_HOME", home)
	host, err := defaultRunnerServiceHost(filepath.Join(home, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if host.StateHome != home || host.LogPath != filepath.Join(home, "runner.log") {
		t.Fatalf("host = %+v, want the service to run and log under %s", host, home)
	}
}
