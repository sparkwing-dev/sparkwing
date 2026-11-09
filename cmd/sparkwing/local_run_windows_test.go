package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/testhome"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestWindowsLocalRunStartsControllerAndFinishes(t *testing.T) {
	cli := buildSubmitCLI(t)
	repo := initSnapshotRepo(t)
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, dir, `package main
import (
 "context"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type hello struct { sw.Base }
func (hello) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
 sw.Job(p, "hello", func(ctx context.Context) error { sw.Info(ctx, "native Windows hello"); return nil })
 return nil
}
func main() { sw.Register("hello", func() sw.Pipeline[sw.NoInputs] { return hello{} }); sw.Main() }
`)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "fixture")
	for _, mode := range []string{"standalone", "shared"} {
		t.Run(mode, func(t *testing.T) {
			user := t.TempDir()
			testhome.Set(t, user)
			home := filepath.Join(user, ".sparkwing")
			t.Setenv("SPARKWING_HOME", home)
			t.Setenv("SPARKWING_SECRETS_KEY_FILE", filepath.Join(home, "secrets.key"))
			t.Setenv("SPARKWING_CONFIG", filepath.Join(user, "missing.yaml"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(user, ".config"))
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := wingdclient.Stop(ctx, wingdclient.Options{Home: home, RetireRunner: true}); err != nil && !errors.Is(err, wingdclient.ErrNoDaemon) {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			args := []string{"-C", repo, "run", "hello", "--sw-workers", "1", "--sw-no-update", "-o", "json"}
			if mode == "standalone" {
				args = append(args, "--sw-allow-unadmitted")
			}
			output, err := exec.CommandContext(ctx, cli, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("run hello: %v\n%s", err, output)
			}
			for line := range strings.SplitSeq(string(output), "\n") {
				var event struct {
					Event string `json:"event"`
					Attrs struct {
						Status string `json:"status"`
					} `json:"attrs"`
				}
				if json.Unmarshal([]byte(line), &event) == nil && event.Event == "run_finish" && event.Attrs.Status == "success" {
					return
				}
			}
			t.Fatalf("missing successful run_finish receipt:\n%s", output)
		})
	}
}
