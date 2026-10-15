//go:build unix

package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// A `--runner here` run is hosted end to end under the one-shot credential
// the trigger minted, through the same claim and secret release as any
// runner: the fence admits every route the host needs, the secret-backed
// guard answers through the trigger's claim, and neither credential reaches
// the node.
func TestTriggerHost_AHereRunnerHostsItsPinnedRunUnderItsOwnCredential(t *testing.T) {
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	bin := podFixture(t)
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	editor, _, err := st.CreateToken("dev@example.com", store.TokenKindUser, store.ScopesForRole(store.RoleEditor), 0, now)
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := st.CreateToken("secrets-owner", store.TokenKindUser, []string{"admin"}, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(controller.New(st, quiet).WithSecretsCipher(cipher).Handler())
	t.Cleanup(srv.Close)
	if err := client.NewWithToken(srv.URL, nil, admin).CreateSecretForPipeline(t.Context(), "DEPLOY_GUARD", "skip", "podnode", true, false); err != nil {
		t.Fatal(err)
	}
	envOut, guard := filepath.Join(t.TempDir(), "env"), filepath.Join(t.TempDir(), "deployed")
	resp, err := client.NewWithToken(srv.URL, nil, editor).CreateTrigger(t.Context(), client.TriggerRequest{
		Pipeline: "podnode", Args: map[string]string{"envout": envOut, "guard": guard},
		Trigger: client.TriggerMeta{Source: "cli"}, Runner: "here", Reason: "INC-42 fleet down",
	})
	if err != nil || resp.RunnerToken == "" {
		t.Fatalf("trigger --runner here = %+v, %v", resp, err)
	}
	here := client.NewWithToken(srv.URL, nil, resp.RunnerToken)
	trig, err := here.ClaimSpecificTrigger(t.Context(), resp.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	if err := HostTriggerRun(ctx, TriggerHost{
		ControllerURL: srv.URL, Token: resp.RunnerToken, Trigger: trig, Entry: pipelineentry.Binary(bin), Dir: t.TempDir(), Logger: quiet,
	}); err != nil {
		t.Fatalf("host: %v", err)
	}
	run, err := st.GetRun(t.Context(), resp.RunID)
	if err != nil || run.Status != "success" {
		t.Fatalf("run = %+v %v, want success", run, err)
	}
	if n, err := st.GetNode(t.Context(), resp.RunID, "deploy"); err != nil || n.Outcome != string(sparkwing.Skipped) {
		t.Fatalf("deploy = %+v %v, want skipped by its secret-backed guard", n, err)
	}
	env, err := os.ReadFile(envOut)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), resp.RunnerToken) || strings.Contains(string(env), store.ClaimTokenPrefix+"_") {
		t.Fatalf("the node's environment holds a controller credential:\n%s", env)
	}
	if tr, err := st.GetTrigger(t.Context(), resp.RunID); err != nil || tr.Status != "done" {
		t.Fatalf("trigger = %+v %v, want done", tr, err)
	}
}
