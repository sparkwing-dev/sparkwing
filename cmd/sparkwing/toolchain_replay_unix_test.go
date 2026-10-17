//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
)

func TestEngineHandoffReplaysRelativeCheckoutAndOpaqueArgs(t *testing.T) {
	args := []string{"-C", "checkout", "run", "build", "--", "--checkout", "pipeline-owned", "--value=a,b"}
	if stage := os.Getenv("SPARKWING_TEST_REPLAY_STAGE"); stage != "" {
		if stage == "select" {
			inv, err := clisurface.Parse("sparkwing", args)
			if err != nil {
				t.Fatal(err)
			}
			toolchainReplayDirectory, err = os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(inv.Value("checkout")); err != nil {
				t.Fatal(err)
			}
			if err := os.Setenv("SPARKWING_TEST_REPLAY_STAGE", "selected"); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if err := replayToolchain(executable, append([]string{"-test.run=^TestEngineHandoffReplaysRelativeCheckoutAndOpaqueArgs$", "--"}, args...), os.Environ()); err != nil {
				t.Fatal(err)
			}
			t.Fatal("engine handoff returned")
		}
		directory, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(struct {
			Directory string
			Args      []string
		}{directory, os.Args[3:]}); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "checkout"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestEngineHandoffReplaysRelativeCheckoutAndOpaqueArgs$")
	command.Dir = directory
	command.Env = append(os.Environ(), "SPARKWING_TEST_REPLAY_STAGE=select")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("handoff: %v: %s", err, out)
	}
	var replay struct {
		Directory string
		Args      []string
	}
	if err := json.Unmarshal(out, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.Directory != directory || !reflect.DeepEqual(replay.Args, args) {
		t.Fatalf("replayed invocation: %+v", replay)
	}
}
