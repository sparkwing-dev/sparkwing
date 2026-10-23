package acceptance_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTypeScriptBoundary(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob("../../sdk/typescript/test/*.test.*")
	if err != nil || len(files) == 0 {
		t.Fatalf("TypeScript test files: %v", err)
	}
	runCell(t, "typescript-sdk-boundary", "node-test", func(t *testing.T) {
		recordLeaf(t)
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
			args = append(args, filepath.Join(root, "test", filepath.Base(file)))
		}
		command := exec.CommandContext(t.Context(), node, args...)
		command.Dir = root
		command.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("TypeScript SDK boundary: %v\n%s", err, output)
		}
	})
	runCell(t, "typescript-native-pipes", "go-nativehost", func(t *testing.T) {
		recordLeaf(t)
		root, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), "go", "test", "-p", "4", "./sdk/typescript/test/nativehost", "-run", "^TestNative")
		command.Dir = root
		command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false", "TMPDIR="+t.TempDir())
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("TypeScript native protocol: %v\n%s", err, output)
		}
	})
}
