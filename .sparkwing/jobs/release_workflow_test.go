package jobs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// releaseTagResolveScript returns the shell the release workflow runs to
// resolve a pushed tag to the commit it publishes.
func releaseTagResolveScript(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	steps := mappingValue(mappingValue(mappingValue(&doc, "jobs"), "validate-tag"), "steps")
	if steps == nil {
		t.Fatal("release.yaml has no validate-tag steps")
	}
	for _, step := range steps.Content {
		if id := mappingValue(step, "id"); id != nil && id.Value == "resolve" {
			return strings.ReplaceAll(mappingValue(step, "run").Value, "${{ github.repository }}", "sparkwing-dev/sparkwing")
		}
	}
	t.Fatal("validate-tag has no resolve step")
	return ""
}

func TestReleaseTagMustBeReachableFromTheDefaultBranch(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not available")
	}
	script := releaseTagResolveScript(t)
	const tagObject = "1111111111111111111111111111111111111111"
	const commit = "2222222222222222222222222222222222222222"
	stubs := t.TempDir()
	for name, body := range map[string]string{
		"git": "#!/bin/sh\nprintf '%s\\trefs/tags/%s\\n%s\\trefs/tags/%s^{}\\n' " + tagObject + " \"$TAG\" " + commit + " \"$TAG\"\n",
		"gh":  "#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$GH_CALLS\"\nif [ \"$COMPARE_STATUS\" = error ]; then exit 1; fi\nprintf '%s\\n' \"$COMPARE_STATUS\"\n",
	} {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		status    string
		published bool
	}{
		{"behind", true},
		{"identical", true},
		{"ahead", false},
		{"diverged", false},
		{"error", false},
	} {
		t.Run(c.status, func(t *testing.T) {
			dir := t.TempDir()
			output, calls := filepath.Join(dir, "output"), filepath.Join(dir, "gh-calls")
			cmd := exec.CommandContext(t.Context(), "bash", "-c", script)
			cmd.Env = append(os.Environ(),
				"PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
				"GITHUB_EVENT_NAME=push", "GITHUB_REF=refs/tags/v0.72.2", "GITHUB_SHA="+tagObject,
				"GITHUB_REPOSITORY=sparkwing-dev/sparkwing", "GITHUB_OUTPUT="+output,
				"TAG=v0.72.2", "DEFAULT_BRANCH=main", "VERIFY_ONLY=false", "INDEX_ONLY=false", "BOOTSTRAP_INDEX=false",
				"COMPARE_STATUS="+c.status, "GH_CALLS="+calls)
			combined, err := cmd.CombinedOutput()
			written, _ := os.ReadFile(output)
			published := err == nil && strings.Contains(string(written), "source_sha="+commit)
			if published != c.published {
				t.Fatalf("compare status %s: published = %v, want %v\nerr: %v\n%s", c.status, published, c.published, err, combined)
			}
			if !c.published && len(written) != 0 {
				t.Errorf("a refused tag still wrote %q", written)
			}
			recorded, _ := os.ReadFile(calls)
			if !strings.Contains(string(recorded), "repos/sparkwing-dev/sparkwing/compare/main..."+commit) {
				t.Errorf("did not compare the tagged commit with main: %q", recorded)
			}
		})
	}
}
