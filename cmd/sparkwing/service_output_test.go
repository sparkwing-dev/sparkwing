package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceOutputDoesNotTrustOpaqueDashboardPID(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "dashboard.pid"), fmt.Appendf(nil, "%d", os.Getpid()), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := outputContractCommand(t, "serve", "status", "--home", home)
	out, err := cmd.Output()
	if err == nil || cmd.ProcessState.ExitCode() != 2 {
		t.Fatalf("opaque PID error: %v", err)
	}
	records := decodeOutputRecords(t, out)
	if len(records) != 1 || records[0]["state"] != "unknown" || records[0]["pid"] != float64(os.Getpid()) || records[0]["home"] != home {
		t.Fatalf("running report: %s", out)
	}
	if _, ok := records[0]["url"]; ok {
		t.Fatalf("missing URL should not become prose in JSON: %s", out)
	}
}

func TestServiceOutputRejectsModesBeforeStart(t *testing.T) {
	for _, args := range [][]string{{"serve", "start"}, {"runs", "consumer", "start"}} {
		for _, flag := range [][]string{{"--output="}, {"-o=jsonl"}, {"--output"}} {
			cmd := outputContractCommand(t, append(append([]string{}, args...), flag...)...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err == nil || len(out) != 0 || !strings.Contains(stderr.String(), "pretty|json|plain") {
				t.Fatalf("%v %v: %v %q %s", args, flag, err, out, &stderr)
			}
			for _, name := range []string{"dashboard.pid", "dashboard.log", "trigger-consumer.pid", "trigger-consumer.log", "trigger-consumer.lock"} {
				if _, err := os.Stat(filepath.Join(cmd.Dir, "state", name)); !os.IsNotExist(err) {
					t.Fatalf("invalid mode touched %s: %v", name, err)
				}
			}
		}
	}
}

func TestHooksStatusOutputRoute(t *testing.T) {
	repo := gateRepo(t)
	installInto(t, (&fakeGit{}).run, repo)
	for _, mode := range []string{"json", "plain", "pretty"} {
		cmd := outputContractCommand(t, "pipeline", "hooks", "status", "--repo", repo, "--output="+mode)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		switch mode {
		case "json":
			records := decodeOutputRecords(t, out)
			if len(records) != 3 || records[len(records)-1]["kind"] != "summary" || records[len(records)-1]["installed"] != float64(2) {
				t.Fatalf("hook records: %s", out)
			}
			for _, record := range records[:len(records)-1] {
				if record["kind"] != "hook" || record["name"] == "" {
					t.Fatalf("hook: %v", record)
				}
			}
		case "plain":
			if string(out) != "post-commit\npre-commit\n" {
				t.Fatalf("plain hooks: %q", out)
			}
		case "pretty":
			if !strings.Contains(string(out), "post-commit -> self-install") {
				t.Fatalf("pretty hooks: %q", out)
			}
		}
	}
}

func TestHooksStatusOutputKeepsHooksWhenConfigFails(t *testing.T) {
	repo := gateRepo(t)
	installInto(t, (&fakeGit{}).run, repo)
	writeRepoFile(t, filepath.Join(repo, ".sparkwing", "sparkwing.yaml"), unloadableProject)
	for _, mode := range []string{"json", "plain", "pretty"} {
		cmd := outputContractCommand(t, "pipeline", "hooks", "status", "--repo", repo, "--output", mode)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil || !strings.Contains(stderr.String(), "config does not load") {
			t.Fatalf("%s: error=%v stderr=%s", mode, err, &stderr)
		}
		switch mode {
		case "json":
			records := decodeOutputRecords(t, out)
			if len(records) != 2 {
				t.Fatalf("missing hooks: %s", out)
			}
			for _, record := range records {
				if record["kind"] != "hook" {
					t.Fatalf("failed config produced a summary: %v", record)
				}
			}
		case "plain":
			if string(out) != "post-commit\npre-commit\n" {
				t.Fatalf("missing hooks: %q", out)
			}
		case "pretty":
			if !strings.Contains(string(out), "pre-commit -> lint") {
				t.Fatalf("missing hooks: %q", out)
			}
		}
	}
}
