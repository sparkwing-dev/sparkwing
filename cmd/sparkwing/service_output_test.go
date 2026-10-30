package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksStatusOutputRoute(t *testing.T) {
	repo := gateRepo(t)
	installInto(t, (&fakeGit{}).run, repo)
	for _, mode := range []string{"json", "plain", "pretty"} {
		cmd := outputContractCommand(t, "-C", repo, "pipeline", "hooks", "status", "--output="+mode)
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
		cmd := outputContractCommand(t, "-C", repo, "pipeline", "hooks", "status", "--output", mode)
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

const unloadableProject = `pipelines:
  - name: e2e/k8s
    entrypoint: Gate
    on:
      pre_commit: {}
`
