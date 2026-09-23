package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestTriggerOwnedAttemptRecordsClaimant(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	identity := store.ClaimIdentity{Principal: "agent:moonborn", TokenPrefix: "runner-token"}
	if err := s.CreateTrigger(ctx, store.Trigger{ID: "trigger-machine", Pipeline: "p", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{Claimant: identity, ClaimGeneration: trigger.ClaimSeq})
	if err := s.CreateRun(ctx, store.Run{ID: trigger.ID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(ctx, store.Node{RunID: trigger.ID, NodeID: "inline", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartNode(ctx, trigger.ID, "inline"); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, trigger.ID, "inline", identity, store.ExecutionStart{ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListNodeExecutionAttempts(ctx, trigger.ID, "inline")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].ExecutorName != "moonborn" || attempts[0].ExecutorLocation != "local" {
		t.Fatalf("trigger owned attempt = %+v, want moonborn on local machine", attempts)
	}
}

func TestMeteredTriggerOwnedAttemptRecordsCloudHost(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	if _, err := s.GrantCredits(ctx, store.CreditGrantFree, 1000*store.MicroCreditsPerCent, "", "operator"); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.CreateTokenWith(ctx, "agent:cloud-helper", store.TokenKindRunner,
		[]string{"triggers.claim"}, time.Hour, time.Now(), store.TokenOptions{Metered: true})
	if err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: "agent:cloud-helper", TokenPrefix: token.Prefix}
	if err := s.CreateTrigger(ctx, store.Trigger{ID: "trigger-cloud", Pipeline: "p", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fenced := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{Claimant: identity, ClaimGeneration: trigger.ClaimSeq})
	if err := s.CreateRun(fenced, store.Run{ID: trigger.ID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(fenced, store.Node{RunID: trigger.ID, NodeID: "inline", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartNode(fenced, trigger.ID, "inline"); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeNodeExecutionStart(fenced, trigger.ID, "inline", identity,
		store.ExecutionStart{ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1, ExecutorName: "cloud-pod-1"}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListNodeExecutionAttempts(ctx, trigger.ID, "inline")
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].ExecutorName != "cloud-pod-1" || attempts[0].ExecutorKind != "cloud" || attempts[0].ExecutorLocation != "cloud" {
		t.Fatalf("metered trigger attempt = %+v, want cloud-pod-1 in cloud", attempts)
	}
}

func TestSchemaV65PreservesExistingGitHubCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO github_runner_credentials (team, prefix, branch, sha, expires_at) VALUES (?, ?, ?, ?, ?)`, string(store.DefaultTeam), "old-token", "main", "abcdef", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`ALTER TABLE github_runner_credentials DROP COLUMN run_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM sparkwing_schema_version WHERE version = 65`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var branch, runID string
	if err := upgraded.DB().QueryRow(`SELECT branch, run_id FROM github_runner_credentials WHERE prefix = ?`, "old-token").Scan(&branch, &runID); err != nil {
		t.Fatal(err)
	}
	if branch != "main" || runID != "" {
		t.Fatalf("migrated credential = %q %q, want main and empty run ID", branch, runID)
	}
}

func TestGitHubRunnerAttemptRecordsWorkflowRun(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	principal := store.GitHubRunnerPrincipalPrefix + "42:acme/widgets"
	tenant, err := s.ForTeam(ctx, store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tenant.AddGitHubRunnerBinding(ctx, store.GitHubRunnerBinding{RepositoryID: 42, RepositoryOwnerID: 7, Repository: "acme/widgets"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := tenant.MintGitHubRunnerCredential(ctx, binding, principal,
		store.GitHubRunnerPush{Branch: "main", SHA: "abcdef", RunID: "555"},
		[]string{"nodes.claim"}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	createRunAndReadyNode(t, s, "github-work", "build")
	identity := store.ClaimIdentity{Principal: principal, TokenPrefix: token.Prefix}
	node, err := s.ClaimNextReadyNode(ctx, identity, "runner:actions-1", time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeNodeExecutionStart(ctx, node.RunID, node.NodeID, identity,
		store.ExecutionStart{HolderID: node.ClaimedBy, ClaimGeneration: node.ClaimGeneration, AttemptOrdinal: 1, ExecutorName: "spoofed-pod"}); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.ListNodeExecutionAttempts(ctx, node.RunID, node.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].ExecutorKind != "github-actions" || attempts[0].ExecutorName != "acme/widgets run 555" {
		t.Fatalf("github attempt = %+v, want acme/widgets run 555", attempts)
	}
	if err := s.CreateTrigger(ctx, store.Trigger{ID: "github-trigger", Pipeline: "p", Status: "pending", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	trigger, err := s.ClaimNextTriggerFor(ctx, identity, time.Minute, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fenced := store.WithTriggerClaimFence(ctx, store.TriggerClaimFence{Claimant: identity, ClaimGeneration: trigger.ClaimSeq})
	if err := s.CreateRun(fenced, store.Run{ID: trigger.ID, Pipeline: "p", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateNode(fenced, store.Node{RunID: trigger.ID, NodeID: "inline", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := s.StartNode(fenced, trigger.ID, "inline"); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeNodeExecutionStart(fenced, trigger.ID, "inline", identity,
		store.ExecutionStart{ClaimGeneration: trigger.ClaimSeq, AttemptOrdinal: 1, ExecutorName: "spoofed-pod"}); err != nil {
		t.Fatal(err)
	}
	inlineAttempts, err := s.ListNodeExecutionAttempts(ctx, trigger.ID, "inline")
	if err != nil {
		t.Fatal(err)
	}
	if len(inlineAttempts) != 1 || inlineAttempts[0].ExecutorName != "acme/widgets run 555" || inlineAttempts[0].ExecutorKind != "github-actions" {
		t.Fatalf("github trigger attempt = %+v, want verified repository and run", inlineAttempts)
	}
}

func TestClaimedAttemptRecordsJobPodAndMeteredRunner(t *testing.T) {
	for _, tc := range []struct {
		name, principal, holder, pod, kind, executor, location string
		metered                                                bool
	}{
		{"job pod", "agent:cluster", "k8s-job:job-a", "job-a-pod-x", "kubernetes", "job-a-pod-x", "cloud", false},
		{"cloud pool", "agent:cloud-helper", "agent:cloud-helper:1", "spoofed-pod", "agent", "cloud-helper", "cloud", true},
		{"pooled agent", "agent:moonborn", "agent:moonborn:1", "spoofed-pod", "agent", "moonborn", "local", false},
		{"runner holder", "runner", "runner:moonborn:1", "spoofed-pod", "agent", "moonborn", "local", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storetest.Open(t)
			ctx := context.Background()
			if tc.metered {
				if _, err := s.GrantCredits(ctx, store.CreditGrantFree, 1000*store.MicroCreditsPerCent, "", "operator"); err != nil {
					t.Fatal(err)
				}
			}
			_, token, err := s.CreateTokenWith(ctx, tc.principal, store.TokenKindRunner, []string{"nodes.claim"}, time.Hour, time.Now(), store.TokenOptions{Metered: tc.metered})
			if err != nil {
				t.Fatal(err)
			}
			createRunAndReadyNode(t, s, "claim-work", "build")
			identity := store.ClaimIdentity{Principal: tc.principal, TokenPrefix: token.Prefix}
			node, err := s.ClaimNextReadyNode(ctx, identity, tc.holder, time.Minute, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.AcknowledgeNodeExecutionStart(ctx, node.RunID, node.NodeID, identity,
				store.ExecutionStart{HolderID: node.ClaimedBy, ClaimGeneration: node.ClaimGeneration, AttemptOrdinal: 1, ExecutorName: tc.pod}); err != nil {
				t.Fatal(err)
			}
			attempts, err := s.ListNodeExecutionAttempts(ctx, node.RunID, node.NodeID)
			if err != nil {
				t.Fatal(err)
			}
			if len(attempts) != 1 || attempts[0].ExecutorKind != tc.kind || attempts[0].ExecutorName != tc.executor || attempts[0].ExecutorLocation != tc.location {
				t.Fatalf("attempt = %+v, want %s %s %s", attempts, tc.kind, tc.executor, tc.location)
			}
		})
	}
}
