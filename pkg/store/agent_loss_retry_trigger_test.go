package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

var agentLossSourceEnv = map[string]string{
	sparkwing.EnvGitHubEventName: sparkwing.EventPullRequest,
	sparkwing.EnvPRNumber:        "42",
	sparkwing.EnvPRAction:        "synchronize",
	sparkwing.EnvPRBaseRef:       "main",
	sparkwing.EnvPRBaseSHA:       "1111111111111111111111111111111111111111",
	sparkwing.EnvPRHeadRef:       "private-feature",
	sparkwing.EnvPRHeadSHA:       "2222222222222222222222222222222222222222",
	"UNRELATED_KEY":              "stays behind",
}

// Planning reads the actor and pull request from the trigger row, so an
// agent-loss retry has to carry both or it plans as an anonymous push.
func TestAgentLossRetryTriggerKeepsActorAndPullRequest(t *testing.T) {
	retry := agentLossRetryTrigger(t, "acme")
	if retry.TriggerUser != "octocat" {
		t.Errorf("retry trigger user = %q, want octocat", retry.TriggerUser)
	}
	want := sparkwing.PullRequestFromEnv(agentLossSourceEnv)
	if got := sparkwing.PullRequestFromEnv(retry.TriggerEnv); !reflect.DeepEqual(got, want) {
		t.Errorf("retry pull request = %+v, want %+v", got, want)
	}
	if retry.TriggerEnv[retryprovenance.PlanHashKey] == "" {
		t.Errorf("retry trigger env %v lost its plan hash", retry.TriggerEnv)
	}
	if _, ok := retry.TriggerEnv["UNRELATED_KEY"]; ok {
		t.Errorf("retry trigger env %v copied a key planning does not read", retry.TriggerEnv)
	}
}

// A trigger of another team that shares the lost run's id must lend the
// retry neither its actor nor its pull request.
func TestAgentLossRetryTriggerIgnoresAnotherTeamsTrigger(t *testing.T) {
	retry := agentLossRetryTrigger(t, "other")
	if retry.TriggerUser != "" {
		t.Errorf("retry trigger user = %q, want none from another team", retry.TriggerUser)
	}
	for key := range agentLossSourceEnv {
		if value, ok := retry.TriggerEnv[key]; ok {
			t.Errorf("retry trigger env carries another team's %s=%q", key, value)
		}
	}
	if retry.TriggerEnv[retryprovenance.PlanHashKey] == "" {
		t.Errorf("retry trigger env %v lost its plan hash", retry.TriggerEnv)
	}
}

// safety: the run is acme's and its same-id trigger triggerTeam's, so a lookup
// that ignores the team reads another team's trigger.
func agentLossRetryTrigger(t *testing.T, triggerTeam store.Team) *store.Trigger {
	t.Helper()
	ctx := context.Background()
	st := storetest.Open(t)
	acme := tenantFor(t, st, "acme")
	plan, _ := json.Marshal(map[string]any{
		"pipeline": "p", "run_id": "run-pr-lost",
		"nodes": []any{map[string]any{"id": "build", "deps": []string{}, "modifiers": map[string]any{"retry": 1}}},
	})
	if err := acme.CreateRun(ctx, store.Run{
		ID: "run-pr-lost", Pipeline: "p", Status: "running", StartedAt: time.Now(), PlanSnapshot: plan,
		RepoURL: "https://example.com/acme/repo.git", GitSHA: strings.Repeat("a", 40),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tenantFor(t, st, triggerTeam).CreateTrigger(ctx, store.Trigger{
		ID: "run-pr-lost", Pipeline: "p", TriggerSource: "github", TriggerUser: "octocat",
		TriggerEnv: agentLossSourceEnv, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "run-pr-lost", NodeID: "build", Status: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNodeReady(ctx, "run-pr-lost", "build"); err != nil {
		t.Fatal(err)
	}
	_, tok, err := acme.CreateToken(ctx, "agent:pr-desktop", store.TokenKindRunner, []string{"nodes.claim"}, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	claimRetryNode(t, st, "run-pr-lost", store.ClaimIdentity{Principal: tok.Principal, TokenPrefix: tok.Prefix}, "agent:pr-desktop:0")
	forceExpireNodeClaim(t, st, "run-pr-lost", "build")

	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(st, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID == "" {
		t.Fatalf("recovery = %+v, want one retry run", recovered)
	}
	retry, err := acme.GetTrigger(ctx, recovered[0].RetryRunID)
	if err != nil {
		t.Fatal(err)
	}
	return retry
}
