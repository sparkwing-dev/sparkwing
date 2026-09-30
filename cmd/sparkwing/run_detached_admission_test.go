package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestDetachedChildOwnsDaemonStartup(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and executes an SDK pipeline")
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("unavailable=%t", unavailable), func(t *testing.T) {
			e := newSubmitTestEnv(t)
			host := e.bin
			if unavailable {
				host = filepath.Join(t.TempDir(), "missing-host")
			}
			e.extraEnv = []string{"SPARKWING_WINGD_BIN=" + host}
			for _, name := range []string{"go.mod", "go.sum"} {
				body, err := os.ReadFile(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				if name == "go.mod" {
					body = []byte(strings.Replace(string(body), "module github.com/sparkwing-dev/sparkwing", "module admissionfixture", 1) +
						fmt.Sprintf("\nrequire github.com/sparkwing-dev/sparkwing v0.0.0\nreplace github.com/sparkwing-dev/sparkwing => %q\n", root))
				}
				if err := os.WriteFile(filepath.Join(e.repoDir, ".sparkwing", name), body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(e.repoDir, ".sparkwing", "main.go"), []byte(admissionFixtureSource), 0o600); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				ack := e.submit()
				st := e.store()
				waitUntil(t, "child admission result", 60*time.Second, func() bool {
					run, err := st.GetRun(t.Context(), ack.RunID)
					return err == nil && (run.Status == "success" || run.Status == "failed")
				})
				run, err := st.GetRun(t.Context(), ack.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if unavailable {
					body, err := os.ReadFile(filepath.Join(e.home, "trigger-consumer.log"))
					if err != nil || !strings.Contains(string(body), "running without local coordination") || !strings.Contains(string(body), "missing-host") {
						t.Fatalf("unavailable host was not reported: %v: %s", err, body)
					}
				} else if _, err := wingdclient.Query(t.Context(), wingdclient.Options{Home: e.home}); err != nil {
					t.Fatalf("child did not start its daemon: %v", err)
				}
				if run.Status != "success" {
					t.Fatalf("child startup result = %s: %s", run.Status, run.Error)
				}
				if _, err := os.Stat(e.marker); err != nil {
					t.Fatalf("successful run did not execute its job: %v", err)
				}
				if unavailable {
					break
				}
			}
		})
	}
}

const admissionFixtureSource = `package main
import (
 "context"
 "os"
 "github.com/sparkwing-dev/sparkwing/sparkwing"
 "github.com/sparkwing-dev/sparkwing/pkg/runner"
)
type Fixture struct { sparkwing.Base }
func (*Fixture) Plan(_ context.Context, p *sparkwing.Plan, _ sparkwing.NoInputs, _ sparkwing.RunContext) error {
 sparkwing.Job(p, "work", func(context.Context) error { return os.WriteFile(os.Getenv("SPARKWING_SUBMIT_TEST_MARKER"), []byte("executed"), 0600) })
 return nil
}
func main() {
 sparkwing.Register("fixture", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Fixture{} })
 runner.Main()
}
`
