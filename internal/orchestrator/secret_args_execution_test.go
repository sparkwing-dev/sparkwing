package orchestrator

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	execSecretValue  = "s3cr3t-token-value"
	execVisibleValue = "prod"
)

func execControllerWithSecretRun(t *testing.T, runID string) (*store.Store, *client.Client) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	err = st.CreateRun(context.Background(), store.Run{
		ID: runID, Pipeline: "deploy", Status: "running",
		GitSHA: "abc123", GitBranch: "main",
		Args: map[string]string{"token": execSecretValue, "env": execVisibleValue},
		Invocation: map[string]any{
			"args":                        map[string]string{"token": execSecretValue, "env": execVisibleValue},
			"reproducer":                  "sparkwing run deploy --token=" + execSecretValue,
			store.InvocationSecretArgsKey: []string{"token"},
		},
		StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	raw, _, err := st.CreateToken("pod", store.TokenKindRunner,
		[]string{controller.ScopeAdmin}, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	srv := httptest.NewServer(controller.New(st, nil).EnableAuthFromStore().Handler())
	t.Cleanup(srv.Close)
	return st, client.NewWithToken(srv.URL, nil, raw)
}

func TestSecretArgs_ExecutionFetchCarriesPlaintextAndSeedsTheMasker(t *testing.T) {
	_, c := execControllerWithSecretRun(t, "run-1")
	ctx := context.Background()

	run, err := c.GetRunForExecution(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetRunForExecution: %v", err)
	}
	if run.Args["token"] != execSecretValue {
		t.Fatalf("pod fetched args[token] = %q, want plaintext -- it would execute with that literal",
			run.Args["token"])
	}

	masker := secrets.NewMasker()
	for _, v := range run.Args {
		masker.Register(v)
	}
	line := "deploying with " + execSecretValue
	if got := masker.Mask(line); strings.Contains(got, execSecretValue) {
		t.Errorf("masker seeded from the execution fetch does not mask the real secret: %q", got)
	}

	payload := []byte(`{"args":{"token":"` + execSecretValue + `"}}`)
	if got := string(maskEventPayload(masker, payload)); strings.Contains(got, execSecretValue) {
		t.Errorf("child_run_start payload not masked: %s", got)
	}
}

func TestSecretArgs_DisplayFetchWouldBreakExecutionAndMasking(t *testing.T) {
	_, c := execControllerWithSecretRun(t, "run-1")

	run, err := c.GetRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Args["token"] != store.RedactedArgValue {
		t.Fatalf("display fetch args[token] = %q, want %q", run.Args["token"], store.RedactedArgValue)
	}
	masker := secrets.NewMasker()
	for _, v := range run.Args {
		masker.Register(v)
	}
	line := "deploying with " + execSecretValue
	if got := masker.Mask(line); !strings.Contains(got, execSecretValue) {
		t.Fatal("expected the display fetch to produce a masker blind to the real secret; " +
			"if this now masks, the execution/display split may be unnecessary")
	}
}

func TestSecretArgs_RetryArgRehydrationUsesTheExecutionView(t *testing.T) {
	_, c := execControllerWithSecretRun(t, "orig-1")

	args := resolveTriggerArgs(context.Background(), c,
		&store.Trigger{Pipeline: "deploy", RetryOf: "orig-1"}, nil)

	if args["token"] != execSecretValue {
		t.Errorf("retry rehydrated args[token] = %q, want plaintext -- the retry would run with that literal",
			args["token"])
	}
	if args["env"] != execVisibleValue {
		t.Errorf("retry lost a non-secret arg: %q", args["env"])
	}
}

func TestRunForExecution_FallsBackForLocalBackends(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	err = st.CreateRun(context.Background(), store.Run{
		ID: "run-1", Pipeline: "deploy", Status: "running",
		Args:      map[string]string{"token": execSecretValue},
		StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runForExecution(context.Background(), localState{st: st}, "run-1")
	if err != nil {
		t.Fatalf("runForExecution: %v", err)
	}
	if run.Args["token"] != execSecretValue {
		t.Errorf("local backend args[token] = %q, want plaintext", run.Args["token"])
	}
}

func TestSecretArgs_RemoteReplaySideloadRoundTripsPlaintext(t *testing.T) {
	remote, c := execControllerWithSecretRun(t, "run-1")
	ctx := context.Background()

	if err := remote.CreateNode(ctx, store.Node{
		RunID: "run-1", NodeID: "ship", Status: "done", Outcome: "success",
	}); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	if err := remote.WriteNodeDispatch(ctx, store.NodeDispatch{
		RunID: "run-1", NodeID: "ship", Seq: 0,
		InputEnvelope: []byte(`{"version":1,"type_name":"Ship"}`),
	}); err != nil {
		t.Fatalf("WriteNodeDispatch: %v", err)
	}

	local, err := store.Open(filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = local.Close() }()

	if err := SideloadRemoteForReplay(ctx, local, c, "run-1", "ship"); err != nil {
		t.Fatalf("SideloadRemoteForReplay: %v", err)
	}

	got, err := local.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetRun after sideload: %v", err)
	}
	if got.Args["token"] != execSecretValue {
		t.Fatalf("sideloaded args[token] = %q, want plaintext -- replay would execute with that literal",
			got.Args["token"])
	}

	if names := got.SecretArgNames(); len(names) != 1 || names[0] != "token" {
		t.Fatalf("sideloaded classification = %v, want [token]", names)
	}
	if got.RedactedForDisplay().Args["token"] != store.RedactedArgValue {
		t.Error("sideloaded run does not redact for display")
	}

	replayID, err := MintReplayRun(ctx, local, "run-1", "ship")
	if err != nil {
		t.Fatalf("MintReplayRun: %v", err)
	}
	replay, err := local.GetRun(ctx, replayID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Args["token"] != execSecretValue {
		t.Errorf("replay args[token] = %q, want plaintext", replay.Args["token"])
	}
	if replay.RedactedForDisplay().Args["token"] != store.RedactedArgValue {
		t.Error("replay run does not redact for display")
	}
}
