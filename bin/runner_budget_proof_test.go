package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRunnerBudgetProofRefusesOutsideDisposableRunner(t *testing.T) {
	testProofRefusesOutsideDisposableRunner(t, "runner-budget-proof.sh")
}

func TestRunnerBudgetProofWorkflowIsManualReadOnlyAndSecretFree(t *testing.T) {
	testProofWorkflow(t, "runner-budget-proof")
}

func TestRunnerBudgetProbe(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	output, err := exec.Command("python3", "-I", "runner_budget_probe_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("probe tests: %v\n%s", err, output)
	}
	t.Logf("%s", output)
}

func TestRunnerBudgetProofVerdict(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	for _, tc := range []struct {
		name              string
		enforced, control float64
		quota             string
		pass              bool
	}{
		{"capped with headroom", 2.05, 3.8, "200000 100000", true},
		{"tolerance boundary", 2.2, 3.8, "200000 100000", true},
		{"original oversubscription", 5.5, 5.5, "200000 100000", false},
		{"over tolerance", 2.201, 3.8, "200000 100000", false},
		{"two-core VM cannot prove it", 2, 2, "200000 100000", false},
		{"quota alone cannot pass", 0, 3.8, "200000 100000", false},
		{"missing kernel quota", 2, 3.8, "max 100000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			paths := []string{filepath.Join(dir, "enforce.json"), filepath.Join(dir, "control.json")}
			for i, cores := range []float64{tc.enforced, tc.control} {
				quota, nice := tc.quota, 10
				if i == 1 {
					quota, nice = "max 100000", 0
				}
				body, err := json.Marshal(map[string]any{
					"cores": cores, "seconds": 20, "cpu_max": quota, "nice": nice,
					"unit": "sparkwing-runner-proof.service", "burners": []int{1, 2, 3, 4, 5, 6},
					"cgroup":         "/sparkwing-runner-proof.service",
					"burner_cgroups": map[string]string{"1": "/sparkwing-runner-proof.service", "2": "/sparkwing-runner-proof.service", "3": "/sparkwing-runner-proof.service", "4": "/sparkwing-runner-proof.service", "5": "/sparkwing-runner-proof.service", "6": "/sparkwing-runner-proof.service"},
					"before":         map[string]int{"usage_usec": 1000},
					"after":          map[string]float64{"usage_usec": 1000 + cores*20*1e6},
				})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths[i], body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			output, err := exec.Command("python3", "runner-budget-probe.py", "judge", paths[0], paths[1]).CombinedOutput()
			if (err == nil) != tc.pass {
				t.Fatalf("pass=%v, want %v: %s", err == nil, tc.pass, output)
			}
		})
	}
}
