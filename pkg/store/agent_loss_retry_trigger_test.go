package store_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/retryprovenance"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Planning reads the actor and pull request from the trigger row, so an
// agent-loss retry has to carry both or it plans as an anonymous push.
func TestAgentLossRetryTriggerKeepsActorAndPullRequest(t *testing.T) {
	st := storetest.Open(t)
	ctx := context.Background()
	createRetryRunAndReadyNode(t, st, "run-pr-lost", 1)
	sourceEnv := map[string]string{
		sparkwing.EnvGitHubEventName: sparkwing.EventPullRequest,
		sparkwing.EnvPRNumber:        "42",
		sparkwing.EnvPRAction:        "synchronize",
		sparkwing.EnvPRBaseRef:       "main",
		sparkwing.EnvPRBaseSHA:       "1111111111111111111111111111111111111111",
		sparkwing.EnvPRHeadRef:       "feature",
		sparkwing.EnvPRHeadSHA:       "2222222222222222222222222222222222222222",
		"UNRELATED_KEY":              "stays behind",
	}
	if err := st.CreateTrigger(ctx, store.Trigger{
		ID: "run-pr-lost", Pipeline: "p", TriggerSource: "github", TriggerUser: "octocat",
		TriggerEnv: sourceEnv, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	identity := store.ClaimIdentity{Principal: "agent:pr-desktop", TokenPrefix: "swr_pr_desktop"}
	claimRetryNode(t, st, "run-pr-lost", identity, "agent:pr-desktop:0")
	forceExpireNodeClaim(t, st, "run-pr-lost", "build")

	recovered, err := store.Maintenance.RecoverExpiredNodeClaims(st, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].RetryRunID == "" {
		t.Fatalf("recovery = %+v, want one retry run", recovered)
	}
	retry, err := st.GetTrigger(ctx, recovered[0].RetryRunID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.TriggerUser != "octocat" {
		t.Errorf("retry trigger user = %q, want octocat", retry.TriggerUser)
	}
	want := sparkwing.PullRequestFromEnv(sourceEnv)
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
