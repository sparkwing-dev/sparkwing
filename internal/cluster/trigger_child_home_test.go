//go:build !windows

package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestTriggerChildOpensItsOwnStore(t *testing.T) {
	hostHome := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeHostHome, err := filepath.Rel(cwd, hostHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_HOME", relativeHostHome)
	t.Setenv("GOCACHE", filepath.Join(hostHome, "gocache"))
	t.Setenv("GOMODCACHE", filepath.Join(hostHome, "gomodcache"))
	t.Setenv("SPARKWING_CHILD_HOST_HOME", hostHome)
	st, err := store.Open(filepath.Join(hostHome, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO sparkwing_requirements (name, added_at, added_by_version)
		VALUES ('future-schema-requirement', 1, 'v99.0.0')`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	workRoot := t.TempDir()
	relativeWorkRoot, err := filepath.Rel(cwd, workRoot)
	if err != nil {
		t.Fatal(err)
	}
	probeFile := filepath.Join(t.TempDir(), "probe")
	t.Setenv("SPARKWING_CHILD_HOME_PROBE", probeFile)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "pipeline")
	contents := fmt.Sprintf("#!/bin/sh\n%q -test.run=^TestTriggerChildStoreProbe$\n%q -test.run=^TestTriggerChildStoreProbe$\n", bin, bin)
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	opts := TriggerLoopOptions{WorkRoot: relativeWorkRoot}
	for _, runID := range []string{"run-a", "run-b"} {
		if err := execHandleTrigger(context.Background(), script, relativeWorkRoot,
			&store.Trigger{ID: runID}, opts, "", discardLogger()); err != nil {
			t.Fatalf("%s: %v", runID, err)
		}
	}
	raw, err := os.ReadFile(probeFile)
	if err != nil {
		t.Fatal(err)
	}
	homes := strings.Fields(string(raw))
	if len(homes) != 4 {
		t.Fatalf("probe homes = %q, want two processes per run", homes)
	}
	if !filepath.IsAbs(homes[0]) || homes[0] == hostHome || homes[0] != homes[1] || homes[2] != homes[3] || homes[0] == homes[2] {
		t.Fatalf("probe homes = %q, want one private home per trigger invocation", homes)
	}
	if want := filepath.Join(hostHome, "tmp"); filepath.Dir(homes[0]) != want || filepath.Dir(homes[2]) != want {
		t.Fatalf("probe homes = %q, want scratch under %s", homes, want)
	}
	for _, home := range []string{homes[0], homes[2]} {
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Errorf("child home %s remains after the run: %v", home, err)
		}
	}
	if st, err := store.Open(filepath.Join(hostHome, "state.db")); err == nil {
		_ = st.Close()
		t.Fatal("host store lost its unknown requirement")
	}
}

func TestTriggerChildStoreProbe(t *testing.T) {
	probeFile := os.Getenv("SPARKWING_CHILD_HOME_PROBE")
	if probeFile == "" {
		t.Skip("child process probe")
	}
	home := os.Getenv("SPARKWING_HOME")
	if home == "" {
		t.Fatal("child has no Sparkwing home")
	}
	st, err := store.Open(filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	hostHome := os.Getenv("SPARKWING_CHILD_HOST_HOME")
	if got := os.Getenv("GOCACHE"); got != filepath.Join(hostHome, "gocache") {
		t.Fatalf("GOCACHE = %q, want the host's shared cache", got)
	}
	if got := os.Getenv("GOMODCACHE"); got != filepath.Join(hostHome, "gomodcache") {
		t.Fatalf("GOMODCACHE = %q, want the host's shared module cache", got)
	}
	f, err := os.OpenFile(probeFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintln(f, home); err != nil {
		t.Fatal(err)
	}
}
