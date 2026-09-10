package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/repos"
)

func TestXrepoListDescribesEachRepositoryOnce(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("GOWORK", "off")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	marker := filepath.Join(t.TempDir(), "describes")
	t.Setenv("SPARKWING_TEST_DESCRIBES", marker)
	var registry strings.Builder
	registry.WriteString("fallback_paths: []\nrepos:\n")
	for i := range 2 {
		root := t.TempDir()
		if _, err := runGit(root, "init", "-q"); err != nil {
			t.Fatal(err)
		}
		sw := filepath.Join(root, ".sparkwing")
		writeRepoFile(t, filepath.Join(sw, "go.mod"), fmt.Sprintf("module example.com/fixture%d\ngo 1.26\n", i))
		writeRepoFile(t, filepath.Join(sw, "main.go"), `package main
import("fmt";"os")
func main(){ f,err:=os.OpenFile(os.Getenv("SPARKWING_TEST_DESCRIBES"),os.O_CREATE|os.O_WRONLY|os.O_APPEND,0600);if err!=nil{panic(err)};fmt.Fprintln(f,"describe");f.Close();fmt.Println("[{\"name\":\"demo\"}]") }
`)
		if _, err := runGit(root, "add", "."); err != nil {
			t.Fatal(err)
		}
		if _, err := runGit(root, "-c", "core.hooksPath="+t.TempDir(), "-c", "commit.gpgsign=false", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&registry, "  - path: %s\n", root)
	}
	registryPath := filepath.Join(t.TempDir(), "repos.yaml")
	writeRepoFile(t, registryPath, registry.String())
	t.Setenv("SPARKWING_REPOS", registryPath)
	repos.InvalidateCache()
	t.Cleanup(repos.InvalidateCache)
	out := captureStdout(t, func() {
		if err := runXrepoList([]string{"-o", "json"}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Count(out, "demo") != 2 {
		t.Fatalf("pipeline discovery did not run: %s", out)
	}
	body, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), "describe"); got != 2 {
		t.Fatalf("described %d times, want once per repository", got)
	}
}
