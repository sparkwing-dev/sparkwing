package jobs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostedTimingExportPreservesOutcomesWithoutPrivateFields(t *testing.T) {
	for _, outcome := range []string{"success", "failed", "cancelled", "running"} {
		t.Run(outcome, func(t *testing.T) {
			dir := t.TempDir()
			handle := filepath.Join(dir, "handle.json")
			binary := filepath.Join(dir, "sparkwing")
			output := filepath.Join(dir, "timings.json")
			if err := os.WriteFile(handle, []byte(`{"run_id":"run-proof"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			fixture := `{"run":{"id":"run-proof","status":"` + outcome + `","git_sha":"abc","invocation":{"args":"PRIVATE_SENTINEL"}},"log_path":"PRIVATE_SENTINEL","nodes":[{"id":"pre-release","status":"` + outcome + `","cpu_nanos":123,"max_rss_bytes":456,"error":"PRIVATE_SENTINEL","steps":[{"step_id":"lint","status":"passed","started_at":"2026-09-15T00:00:00Z"},{"step_id":"store-postgres","status":"` + outcome + `","started_at":"2026-09-15T00:00:01Z","error":"PRIVATE_SENTINEL"}]}]}`
			stub := "#!/bin/sh\ncat <<'JSON'\n" + fixture + "\nJSON\n"
			if outcome != "success" {
				stub += "case \" $* \" in *' --exit-zero '*) exit 0;; *) exit 1;; esac\n"
			}
			if err := os.WriteFile(binary, []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "../../bin/export-hosted-run-timings.sh", handle, binary, output)
			if data, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("export: %v\n%s", err, data)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "PRIVATE_SENTINEL") {
				t.Fatal("export contains private fields")
			}
			var result struct {
				Availability string                  `json:"availability"`
				Run          struct{ Status string } `json:"run"`
				Nodes        []struct {
					CPU   int64 `json:"cpu_nanos"`
					Steps []struct {
						ID         string  `json:"step_id"`
						Status     string  `json:"status"`
						FinishedAt *string `json:"finished_at"`
					} `json:"steps"`
				} `json:"nodes"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if result.Availability != "available" || result.Run.Status != outcome || len(result.Nodes) != 1 {
				t.Fatalf("lost run outcome: %s", data)
			}
			if result.Nodes[0].CPU != 123 || len(result.Nodes[0].Steps) != 2 ||
				result.Nodes[0].Steps[0].ID != "lint" || result.Nodes[0].Steps[0].Status != "passed" ||
				result.Nodes[0].Steps[1].ID != "store-postgres" || result.Nodes[0].Steps[1].Status != outcome ||
				result.Nodes[0].Steps[1].FinishedAt != nil {
				t.Fatalf("lost measurements or invented completion: %s", data)
			}
		})
	}
}

func TestHostedTimingExportReportsMissingRun(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "timings.json")
	cmd := exec.Command("bash", "../../bin/export-hosted-run-timings.sh", filepath.Join(dir, "missing"), "/unused", output)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("export: %v\n%s", err, data)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Availability string `json:"availability"`
		Reason       string `json:"reason"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Availability != "unavailable" || result.Reason != "no_run_handle" {
		t.Fatalf("missing run presented as evidence: %s", data)
	}
}

func TestHostedTimingExportRejectsWrongRun(t *testing.T) {
	dir := t.TempDir()
	handle := filepath.Join(dir, "handle.json")
	binary := filepath.Join(dir, "sparkwing")
	output := filepath.Join(dir, "timings.json")
	if err := os.WriteFile(handle, []byte(`{"run_id":"expected"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' '{\"run\":{\"id\":\"other\"},\"nodes\":[]}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "../../bin/export-hosted-run-timings.sh", handle, binary, output)
	if data, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("accepted another run: %s", data)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("published invalid evidence: %v", err)
	}
}

func TestCanonicalWorkflowRetainsTimingForEveryOutcome(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/canonical-gates.yaml")
	requireWorkflowText(t, body,
		"- name: Export canonical run timings\n        if: ${{ always() }}",
		"- name: Retain canonical run timings\n        if: ${{ always() }}",
		"name: canonical-timings-${{ matrix.gate }}-${{ github.run_id }}-${{ github.run_attempt }}",
		"path: ${{ runner.temp }}/canonical-timings.json",
		"retention-days: 30",
		`cp bin/export-hosted-run-timings.sh "$RUNNER_TEMP/export-hosted-run-timings.sh"`,
	)
}
