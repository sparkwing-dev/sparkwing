package cluster

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestTriggerCompilesDeclaredSourceAndExecutesSubject(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and executes trigger source")
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	selected := t.TempDir()
	if err := os.Mkdir(filepath.Join(selected, ".sparkwing"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".sparkwing/go.mod": "module fixture\n\ngo 1.22\n",
		".sparkwing/main.go": `package main
import "os"
func main(){body,err:=os.ReadFile("subject");if err!=nil{panic(err)};if err:=os.WriteFile(os.Getenv("SOURCE_OUTPUT"),body,0600);err!=nil{panic(err)}}
`,
		"subject": "main",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(selected, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.email=test@example.test", "-c", "user.name=Test", "commit", "-qm", "source"}} {
		cmd := exec.Command("git", append([]string{"-C", selected}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	out, err := exec.Command("git", "-C", selected, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.TrimSpace(string(out))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	previous, previousRef := fetchSourceFn, fetchPipelineRefFn
	t.Cleanup(func() { fetchSourceFn, fetchPipelineRefFn = previous, previousRef })
	for _, source := range []string{"origin/main", "refs/heads/main", "refs/tags/main", revision} {
		t.Run(source, func(t *testing.T) {
			execution := t.TempDir()
			if err := os.Mkdir(filepath.Join(execution, ".sparkwing"), 0o700); err != nil {
				t.Fatal(err)
			}
			cfg := fmt.Sprintf("pipelines:\n - name: fixture\n   entrypoint: Fixture\n   source: %s\n", source)
			if err := os.WriteFile(filepath.Join(execution, ".sparkwing", "sparkwing.yaml"), []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(execution, "subject"), []byte("branch"), 0o600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(t.TempDir(), "result")
			t.Setenv("SOURCE_OUTPUT", output)
			calls := 0
			fetchSourceFn = func(_, _, _, _, _, _, _ string) (string, error) {
				calls++
				return filepath.Join(execution, ".sparkwing"), nil
			}
			fetchPipelineRefFn = func(_ context.Context, _, _, _, _, ref, _ string) (string, error) {
				calls++
				if ref != source {
					t.Errorf("declared ref changed: %q, want %q", ref, source)
				}
				return filepath.Join(selected, ".sparkwing"), nil
			}

			trigger := &store.Trigger{ID: "source-run", Pipeline: "fixture", RepoURL: "https://example.test/team/repo.git", GitBranch: "feature"}
			opts := TriggerLoopOptions{WorkRoot: t.TempDir(), ControllerURL: server.URL, GitcacheURL: server.URL + "/api/v1/gitcache", Token: "test-token"}
			if _, err := handleOneTrigger(t.Context(), client.NewWithToken(server.URL, nil, "test-token"), trigger, opts, logger); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(output)
			if err != nil || string(body) != "branch" {
				t.Fatalf("execution subject: %q, %v", body, err)
			}
			if calls != 2 || trigger.TriggerEnv[orchestrator.PipelineRevKey] != revision {
				t.Fatalf("source not pinned: %d %v", calls, trigger.TriggerEnv)
			}
		})
	}
}
