package match

import (
	"os"
	"path/filepath"
	"time"
)

// KnownTools is every tool an agent detects on its PATH and advertises as a
// tool:<name> label. A node names one of these to run only where it exists.
var KnownTools = []string{
	"aws", "buildx", "crane", "docker", "git", "go", "golangci-lint",
	"helm", "kubectl", "node", "npm", "shellcheck", "terraform",
}

// CloudTools is the subset of [KnownTools] the Sparkwing Cloud runner image
// provides. build/runner-tools declares the same list, and the image build
// fails when the image lacks one of them.
var CloudTools = []string{"git", "go"}

// ToolPrefix starts every tool label.
const ToolPrefix = "tool:"

// DefaultClaimWait is how long a ready node waits for an agent to claim it
// before the controller fails it as unclaimable. A pipeline may set its own.
const DefaultClaimWait = 24 * time.Hour

// IsKnownTool reports whether name is in [KnownTools].
func IsKnownTool(name string) bool {
	for _, known := range KnownTools {
		if known == name {
			return true
		}
	}
	return false
}

// ToolLabels turns tool names into their tool:<name> labels.
func ToolLabels(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, ToolPrefix+name)
	}
	return out
}

// DetectTools returns the tool labels of the [KnownTools] that lookPath finds.
// Self-reported, so they steer scheduling and grant nothing.
func DetectTools(lookPath func(string) (string, error)) []string {
	var found []string
	for _, name := range KnownTools {
		if _, err := lookPath(name); err == nil || (name == "buildx" && buildxPlugin(lookPath)) {
			found = append(found, name)
		}
	}
	return ToolLabels(found)
}

// bug: buildx normally installs as a docker CLI plugin outside PATH.
func buildxPlugin(lookPath func(string) (string, error)) bool {
	if _, err := lookPath("docker-buildx"); err == nil {
		return true
	}
	dirs := []string{"/usr/local/lib/docker/cli-plugins", "/usr/libexec/docker/cli-plugins", "/usr/lib/docker/cli-plugins"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".docker", "cli-plugins"))
	}
	for _, dir := range dirs {
		if info, err := os.Stat(filepath.Join(dir, "docker-buildx")); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}
