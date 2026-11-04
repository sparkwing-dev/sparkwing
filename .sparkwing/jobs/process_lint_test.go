package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// safety: the fixture imports os/exec under an alias, the case the deleted
// spawn guard covered, and checks the per-path exclusions keep the rule.
func TestLintRefusesRawProcessConstructorsOutsideProcenv(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: runs golangci-lint; the fast class runs under -short")
	}
	lint, err := exec.LookPath("golangci-lint")
	if err != nil {
		t.Skip("golangci-lint is not on PATH")
	}
	raw, err := os.ReadFile(filepath.Join(sourceTreeRoot(), ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	linters := config["linters"].(map[string]any)
	linters["default"], linters["enable"] = "none", []string{"forbidigo"}
	delete(config, "issues")
	delete(config, "formatters")
	body, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	spawn := "package %s\n\nimport run \"os/exec\"\n\nfunc Spawn() error { return run.Command(\"true\").Run() }\n"
	for path, src := range map[string]string{
		"go.mod":                      "module fixture\n\ngo 1.26\n",
		"lint.yml":                    string(body),
		"lib/lib.go":                  strings.Replace(spawn, "%s", "lib", 1),
		"cmd/tool/tool.go":            strings.Replace(spawn, "%s", "tool", 1) + "\nfunc Crash() { panic(\"allowed in cmd\") }\n",
		"internal/procenv/command.go": strings.Replace(spawn, "%s", "procenv", 1),
	} {
		writeGoFile(t, filepath.Join(dir, path), src)
	}
	cmd := exec.Command(lint, "run", "--config", filepath.Join(dir, "lint.yml"), "--allow-parallel-runners", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false")
	out, _ := cmd.CombinedOutput()
	report := string(out)
	for _, want := range []string{"lib/lib.go", "cmd/tool/tool.go"} {
		if !strings.Contains(report, want) || !strings.Contains(report, "raw process constructor") {
			t.Errorf("lint did not refuse the aliased constructor in %s:\n%s", want, report)
		}
	}
	if strings.Contains(report, "internal/procenv/command.go") || strings.Contains(report, "panic(") {
		t.Errorf("lint refused procenv's own constructor or a panic in cmd:\n%s", report)
	}
}
