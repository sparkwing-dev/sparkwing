package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestTypeScriptScaffoldChecksNodeImports(t *testing.T) {
	if testing.Short() {
		t.Skip("installs scaffold dependencies and runs the TypeScript compiler")
	}
	for _, tool := range []string{"node", "npm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("requires %s: %v", tool, err)
		}
	}
	for _, template := range []string{"typescript", "merge-queue-typescript"} {
		t.Run(template, func(t *testing.T) {
			repo := t.TempDir()
			t.Chdir(repo)
			var err error
			captureStdout(t, func() {
				err = runSparkwing([]string{"pipeline", "new", "--pipeline", "hello", "--template", template, "-o", "json"})
			})
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(repo, ".sparkwing")
			const source = `import { readFileSync } from "node:fs";
import { join } from "node:path";
export const contents: string = readFileSync(join(process.cwd(), "package.json"), "utf8");
`
			if err := os.WriteFile(filepath.Join(dir, "node-imports.ts"), []byte(source), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{
				{"ci", "--ignore-scripts", "--no-audit", "--no-fund"},
				{"exec", "--offline", "--no", "--", "tsc", "--noEmit"},
			} {
				cmd := procenv.SDKCommandContext(t.Context(), os.Environ(), dir, "npm", args...)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("npm %v: %v\n%s", args, err, output)
				}
			}
		})
	}
}
