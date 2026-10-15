package acceptance_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
)

func typeScriptBoundary(t *testing.T, scenario string) {
	t.Helper()
	files := acceptance.TypeScriptTests(scenario)
	if len(files) == 0 {
		return
	}
	t.Run("typescript_sdk_boundary", func(t *testing.T) {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Fatal("TypeScript SDK acceptance requires Node.js 22.18 or later")
		}
		root, err := filepath.Abs("../../sdk/typescript")
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"--test", "--experimental-test-isolation=none"}
		for _, file := range files {
			args = append(args, filepath.Join(root, "test", file))
		}
		command := exec.CommandContext(t.Context(), node, args...)
		command.Dir = root
		command.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("TypeScript SDK boundary: %v\n%s", err, output)
		}
	})
}
