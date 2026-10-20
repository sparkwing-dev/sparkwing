//go:build !windows

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestNativeHereTriggerSubmitsAndReplaysRecordedReason(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	if err := os.WriteFile(os.Getenv("SPARKWING_CONFIG"), []byte("run:\n  submit_env_allow: [W9_APP_*, W9_MISSING, W9_TOKEN]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("W9_APP_REGION", "submitted-region-value")
	t.Setenv("W9_APP_EMPTY", "")
	t.Setenv("W9_TOKEN", "must-never-be-submitted")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	repo := initSnapshotRepo(t)
	dir := filepath.Join(repo, ".sparkwing")
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n", 0o600)
	writeSDKFixture(t, dir, `package main
import (
 "context"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type pipeline struct{ sw.Base }
func (pipeline) Plan(_ context.Context, p *sw.Plan, _ sw.NoInputs, _ sw.RunContext) error {
 sw.Job(p, "verify", func(context.Context) error { return nil })
 return nil
}
func main() { sw.Register("Deploy", func() sw.Pipeline[sw.NoInputs] { return pipeline{} }); sw.Main() }
`)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "here source")
	runSnapshotGit(t, repo, "remote", "add", "origin", "https://git.example/acme/app.git")
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	t.Chdir(repo)
	flags, err := parseTriggerFlagsArgs([]string{"deploy-prod", "--profile=prod", "--runner=here", "--reason=investigate deployment"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := prepareNativeTrigger(t.Context(), flags)
	if err != nil {
		t.Fatalf("native preparation: %v", err)
	}
	if request.Request.Options.Runner.Value.Kind != runspec.RunnerHere {
		t.Fatalf("selection = %+v", request.Request.Options.Runner)
	}
	if len(request.SubmittedEnv) != 2 || string(request.SubmittedEnv["W9_APP_REGION"]) != "submitted-region-value" {
		t.Fatal("native trigger did not filter submitted environment")
	}
	if err := runspec.VerifyEnvironment(request.Request.Env, request.SubmittedEnv); err != nil {
		t.Fatal(err)
	}
	if got := request.Request.Reason; got != flags.reason {
		t.Fatalf("reason=%q, want %q", got, flags.reason)
	}
	_, server, bearer := burstIntakeController(t, "http://controller.test")
	handler := server.Handler()
	controller, err := client.NewEngineClient(client.EngineOptions{URL: "http://controller.test", Bearer: func(context.Context) (string, error) { return bearer, nil }, HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder.Result(), nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	created, err := controller.CreateRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if created.HereBearer == "" || created.Request.Request.Reason != flags.reason {
		t.Fatal("intake lost reason or first here bearer")
	}
	replay, err := controller.CreateRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.RunID != created.RunID || replay.HereBearer != "" || replay.Request.Hash != created.Request.Hash || replay.Request.Request.Reason != flags.reason {
		t.Fatal("replay changed sealed reason or here authority")
	}
	carried, err := runspec.CarryRetry(runspec.RetryInput{Request: created.Request, SourceRunID: created.RunID})
	if err != nil || carried.Request.Request.Reason != flags.reason || carried.Request.Hash != created.Request.Hash {
		t.Fatalf("retry lost reason or request seal: %v", err)
	}
	request.Request.Reason = "different reason"
	if _, err := controller.CreateRun(t.Context(), request); err == nil {
		t.Fatal("changed sealed reason replayed under the same intake key")
	}
}
