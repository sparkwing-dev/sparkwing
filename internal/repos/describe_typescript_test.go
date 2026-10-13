//go:build unix

package repos

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func typeScriptRepo(t *testing.T, pipeline string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH")
	}
	sdk, err := filepath.Abs(filepath.Join("..", "..", "sdk", "typescript"))
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "@sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sdk, filepath.Join(dir, "node_modules", "@sparkwing", "sdk")); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"package.json": `{"type":"module","main":"pipeline.ts","dependencies":{"@sparkwing/sdk":"file:` + sdk + `"}}`,
		"pipeline.ts":  pipeline,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

func TestDescribeRepoRunsATypeScriptPipelineBehindTheBootstrap(t *testing.T) {
	repo := typeScriptRepo(t, `import { definePipeline, main } from "@sparkwing/sdk";
definePipeline({ name: "ts-hello", plan(plan) { plan.job("build", () => {}); } });
process.exitCode = await main();
`)
	got, err := DescribeRepo(repo)
	if err != nil {
		t.Fatal(err)
	}
	if names := pipelineNames(got); len(names) != 1 || names[0] != "ts-hello" {
		t.Fatalf("pipelines = %v", names)
	}
	if names, ok := PipelineNamesIfBuilt(repo); !ok || len(names) != 1 || names[0] != "ts-hello" {
		t.Fatalf("PipelineNamesIfBuilt = %v, %v", names, ok)
	}
}
