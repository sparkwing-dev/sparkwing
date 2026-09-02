package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestSubmissionEnvironmentIsOwnerOnlyAndDiscarded(t *testing.T) {
	home := t.TempDir()
	const runID = "run-environment"
	if err := CaptureSubmissionEnvironment(home, runID, []string{"SPARKWING_PROFILE=dev", "PATH=/submit/bin"}, quietLogger()); err != nil {
		t.Fatal(err)
	}

	path := submissionEnvironmentPath(home, runID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("snapshot mode = %04o, want 0600", got)
	}
	env, err := submissionEnvironment(home, &store.Trigger{
		ID: runID,
		TriggerEnv: map[string]string{
			SubmissionEnvironmentCapturedKey: "1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 2 || env[0] != "SPARKWING_PROFILE=dev" || env[1] != "PATH=/submit/bin" {
		t.Fatalf("snapshot = %#v", env)
	}

	if err := DiscardSubmissionEnvironment(home, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("snapshot still exists after discard: %v", err)
	}
}

func TestSubmissionExecutionEnvironmentForcesHomeAndDropsRunControls(t *testing.T) {
	env := submissionExecutionEnvironment([]string{
		"PATH=/submit/bin",
		"SPARKWING_HOME=/wrong",
		"SPARKWING_RUN_HANDLE_FILE=/tmp/stale.json",
		"SPARKWING_START_AT=late",
		"SPARKWING_SUBMIT_TEST_ENV=kept",
	}, "/queue/home")
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"/wrong", "SPARKWING_RUN_HANDLE_FILE", "SPARKWING_START_AT"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("execution environment contains %q: %s", forbidden, joined)
		}
	}
	for _, required := range []string{"PATH=/submit/bin", "SPARKWING_HOME=/queue/home", "SPARKWING_SUBMIT_TEST_ENV=kept"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("execution environment omits %q: %s", required, joined)
		}
	}
}

func TestReconcileSubmissionEnvironmentsRemovesTerminalSnapshots(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	const runID = "run-terminal-environment"
	if err := CaptureSubmissionEnvironment(home, runID, []string{"SPARKWING_PROFILE=dev"}, quietLogger()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: runID, Pipeline: "build", Status: "pending", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := st.CancelPendingTrigger(ctx, runID); err != nil || !cancelled {
		t.Fatalf("cancel pending trigger = %v, %v", cancelled, err)
	}
	removed, err := ReconcileSubmissionEnvironments(ctx, home, st, 100)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(submissionEnvironmentPath(home, runID)); !os.IsNotExist(err) {
		t.Fatalf("terminal snapshot still exists: %v", err)
	}
}

func TestReconcileSubmissionEnvironmentsRemovesMalformedResidueAndContinues(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	dir := filepath.Join(home, submissionEnvironmentDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "000-malformed.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	const runID = "run-terminal-after-malformed"
	if err := CaptureSubmissionEnvironment(home, runID, []string{"SPARKWING_PROFILE=dev"}, quietLogger()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.CreateTrigger(ctx, store.Trigger{ID: runID, Pipeline: "build", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := st.CancelPendingTrigger(ctx, runID); err != nil || !cancelled {
		t.Fatalf("cancel pending trigger = %v, %v", cancelled, err)
	}
	removed, err := ReconcileSubmissionEnvironments(ctx, home, st, 100)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want malformed and terminal snapshots", removed)
	}
}

func TestReconcileSubmissionEnvironmentsRemovesAbandonedTemporaryFile(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	dir := filepath.Join(home, submissionEnvironmentDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".submission-environment-crash")
	if err := os.WriteFile(path, []byte(`{"environment":["SPARKWING_PROFILE=dev"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-abandonedSubmissionEnvironmentAge - time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	removed, err := ReconcileSubmissionEnvironments(context.Background(), home, st, 100)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
}

func TestReconcileSubmissionEnvironmentsPreservesFreshSnapshotBeforeTriggerCommit(t *testing.T) {
	home := t.TempDir()
	st := consumerTestStore(t, home)
	const runID = "run-capture-window"
	if err := CaptureSubmissionEnvironment(home, runID, []string{"MODE=fresh"}, quietLogger()); err != nil {
		t.Fatal(err)
	}
	removed, err := ReconcileSubmissionEnvironments(context.Background(), home, st, 100)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want fresh pre-trigger snapshot preserved", removed)
	}
	if _, err := os.Stat(submissionEnvironmentPath(home, runID)); err != nil {
		t.Fatalf("fresh pre-trigger snapshot: %v", err)
	}
}

func TestSubmissionWithoutCapturedEnvironmentUsesConsumerEnvironment(t *testing.T) {
	env, err := submissionEnvironment(t.TempDir(), &store.Trigger{ID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if env != nil {
		t.Fatalf("legacy submission environment = %#v, want nil", env)
	}
}

func TestCaptureSubmissionEnvironmentKeepsOnlyAllowedNonCredentialVariables(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want []string
	}{
		{
			name: "keeps the dispatch snapshot shape",
			env:  []string{"SPARKWING_PROFILE=dev", "GITHUB_REF_NAME=main", "PATH=/bin", "HOME=/u", "AWS_REGION=us-east-1"},
			want: []string{"SPARKWING_PROFILE=dev", "GITHUB_REF_NAME=main", "PATH=/bin", "HOME=/u"},
		},
		{
			name: "drops credential-shaped names",
			env:  []string{"GITHUB_TOKEN=gh", "SPARKWING_SECRETS_KEY=k", "AWS_SECRET_ACCESS_KEY=a", "SPARKWING_PROFILE=dev"},
			want: []string{"SPARKWING_PROFILE=dev"},
		},
		{
			name: "drops credential-shaped values",
			env:  []string{"SPARKWING_HEADER=Bearer abc", "SPARKWING_DB=postgres://u:p@h/db", "SPARKWING_PROFILE=dev"},
			want: []string{"SPARKWING_PROFILE=dev"},
		},
		{
			name: "honours the operator allow-list",
			env: []string{
				submissionEnvironmentAllowKey + "=AWS_REGION,DOCKER_*",
				"AWS_REGION=us-east-1", "DOCKER_HOST=tcp://h:1", "DOCKER_PASSWORD=p", "LANG=C",
			},
			want: []string{
				submissionEnvironmentAllowKey + "=AWS_REGION,DOCKER_*",
				"AWS_REGION=us-east-1", "DOCKER_HOST=tcp://h:1",
			},
		},
		{
			name: "carries the allow-listed agent socket and docker certificate directory",
			env: []string{
				submissionEnvironmentAllowKey + "=SSH_AUTH_SOCK,DOCKER_CERT_PATH",
				"SSH_AUTH_SOCK=/tmp/agent.sock", "DOCKER_CERT_PATH=/home/u/.docker",
			},
			want: []string{
				submissionEnvironmentAllowKey + "=SSH_AUTH_SOCK,DOCKER_CERT_PATH",
				"SSH_AUTH_SOCK=/tmp/agent.sock", "DOCKER_CERT_PATH=/home/u/.docker",
			},
		},
		{
			name: "drops a credential hidden in a URL query or path",
			env: []string{
				"SPARKWING_CACHE_URL=https://cache.example.com/?sig=abc",
				"SPARKWING_WEBHOOK=https://hooks.example.com/services/T0/zzzSECRET",
				"SPARKWING_HDR=x\nAuthorization: Bearer abc",
				"SPARKWING_SA={\"type\":\"service_account\",\"api_key\":\"a\"}",
				"SPARKWING_PROFILE=dev",
			},
			want: []string{"SPARKWING_PROFILE=dev"},
		},
		{
			name: "drops a personal access token named outside the substring set",
			env:  []string{"GITHUB_PAT=ghp_live", "SPARKWING_PROFILE=dev"},
			want: []string{"SPARKWING_PROFILE=dev"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			const runID = "run-filter"
			if err := CaptureSubmissionEnvironment(home, runID, tc.env, quietLogger()); err != nil {
				t.Fatal(err)
			}
			got, err := submissionEnvironment(home, &store.Trigger{
				ID:         runID,
				TriggerEnv: map[string]string{SubmissionEnvironmentCapturedKey: "1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("captured environment = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCaptureSubmissionEnvironmentRejectsABareWildcard(t *testing.T) {
	err := CaptureSubmissionEnvironment(t.TempDir(), "run-wildcard",
		[]string{submissionEnvironmentAllowKey + "=*", "AWS_REGION=us-east-1"}, quietLogger())
	if err == nil {
		t.Fatal("CaptureSubmissionEnvironment error = nil, want a rejection of the bare wildcard")
	}
	if !strings.Contains(err.Error(), submissionEnvironmentAllowKey) {
		t.Fatalf("error = %v, want it to name %s", err, submissionEnvironmentAllowKey)
	}
}

func TestConsumeSubmissionEnvironmentDeletesTheSnapshotAtRunStart(t *testing.T) {
	home := t.TempDir()
	const runID = "run-consume"
	if err := CaptureSubmissionEnvironment(home, runID, []string{"SPARKWING_PROFILE=dev"}, quietLogger()); err != nil {
		t.Fatal(err)
	}
	trig := &store.Trigger{
		ID:         runID,
		TriggerEnv: map[string]string{SubmissionEnvironmentCapturedKey: "1"},
	}
	env, err := consumeSubmissionEnvironment(home, trig, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(env, []string{"SPARKWING_PROFILE=dev"}) {
		t.Fatalf("consumed environment = %#v", env)
	}
	if _, err := os.Stat(submissionEnvironmentPath(home, runID)); !os.IsNotExist(err) {
		t.Fatalf("snapshot outlived the start of the run: %v", err)
	}
	again, againErr := consumeSubmissionEnvironment(home, trig, quietLogger())
	if againErr == nil {
		t.Fatalf("redispatch environment = %#v, want a fail-closed error", again)
	}
	if again != nil {
		t.Fatalf("redispatch environment = %#v, want nothing", again)
	}
}

func TestRequeuedRunDoesNotInheritTheConsumerShell(t *testing.T) {
	t.Setenv("SPARKWING_DRY_RUN", "1")
	t.Setenv("SPARKWING_ONLY", "deploy")
	t.Setenv("SPARKWING_HOME", "/consumer/home")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "consumer-shell-secret")

	home := t.TempDir()
	const runID = "run-requeue"
	if err := CaptureSubmissionEnvironment(home, runID, []string{"SPARKWING_PROFILE=dev"}, quietLogger()); err != nil {
		t.Fatal(err)
	}
	trig := &store.Trigger{ID: runID, TriggerEnv: map[string]string{SubmissionEnvironmentCapturedKey: "1"}}
	if _, err := consumeSubmissionEnvironment(home, trig, quietLogger()); err != nil {
		t.Fatal(err)
	}

	env, err := consumeSubmissionEnvironment(home, trig, quietLogger())
	if err == nil {
		t.Fatalf("requeued dispatch = %#v, want the missing snapshot to fail the run", env)
	}

	joined := strings.Join(submissionExecutionEnvironment(nil, home), "\n")
	for _, forbidden := range []string{"SPARKWING_DRY_RUN", "SPARKWING_ONLY", "/consumer/home"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("uncaptured dispatch environment carries %q", forbidden)
		}
	}
	if !strings.Contains(joined, "SPARKWING_HOME="+home) {
		t.Fatalf("uncaptured dispatch environment does not force SPARKWING_HOME: %s", joined)
	}
}
