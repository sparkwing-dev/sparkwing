package controller_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func floodMember(t *testing.T, f *floodFixture, team store.Team, name string) string {
	t.Helper()
	raw, _, err := teststore.ProvisionCredential(t.Context(), f.st, capability.ProvisionSpec{
		Kind: capability.ProvisionMember, Team: string(team), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: name},
		Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite, capability.RunsControl},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestFloodPolicy_APISubmissionsAndRetriesUsePerTokenCaps(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{RunsPerPrincipalHour: 1})
	tokens := []string{f.token, floodMember(t, f, "default", "alice"), floodMember(t, f, "default", "alice")}
	first := floodRun(t, f.submit(tokens[0], "build", fortyHex(1)), 201)
	if err := teststore.SeedRunFinish(f.st, t.Context(), store.DefaultTeam, first.RunID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	retry := func(token, key string) *httptest.ResponseRecorder {
		body, err := json.Marshal(controllerwire.RetryRequest{Mode: "all", IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/api/v1/runs/"+first.RunID+"/retry", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		return f.serve(req)
	}
	floodRun(t, retry(tokens[1], "inside-cap"), 200)
	floodRun(t, f.submit(tokens[2], "build", fortyHex(3)), 201)
	if r := f.submit(tokens[0], "build", fortyHex(4)); r.Code != 429 {
		t.Fatalf("first token escaped its cap: %d %s", r.Code, r.Body)
	}
	if r := retry(tokens[2], "over-cap"); r.Code != 429 {
		t.Fatalf("retry = %d %s", r.Code, r.Body)
	}
	runs, err := f.st.AsOperator().ListRuns(t.Context(), store.RunFilter{Limit: 10})
	if err != nil || len(runs) != 3 {
		t.Fatalf("runs = %d: %v", len(runs), err)
	}
}

func TestFloodPolicy_ShedsOnlyTheTeamWhoseQueueIsDeep(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{})
	for i := range 2 {
		floodRun(t, f.submit(f.token, "build", fortyHex(i)), 201)
	}
	teststore.SeedTeam(t, f.st, "other")
	other := floodMember(t, f, "other", "ci-other")
	f.srv.WithFloodPolicy(controller.FloodPolicy{ShedQueueDepth: 2})
	floodRun(t, f.submit(other, "build", fortyHex(1)), 201)
	if r := f.submit(f.token, "build", fortyHex(2)); r.Code != 503 {
		t.Fatalf("backlogged team = %d %s", r.Code, r.Body)
	}
}

func TestFloodPolicyTeamBucketsRemainIndependentProperty(t *testing.T) {
	f := newFloodServer(t, controller.FloodPolicy{RunsPerPrincipalHour: 1, DedupeWindow: time.Minute})
	i := 0
	property := func(seed uint32) bool {
		i++
		teams := []store.Team{store.Team(fmt.Sprintf("a-%d-%d", i, seed)), store.Team(fmt.Sprintf("b-%d-%d", i, seed))}
		f.srv.WithFloodPolicy(controller.FloodPolicy{RunsPerPrincipalHour: 1, DedupeWindow: time.Minute})
		for _, team := range teams {
			if _, err := f.st.ForTeam(t.Context(), team); err != nil {
				teststore.SeedTeam(t, f.st, team)
			}
		}
		a, b := floodMember(t, f, teams[0], fmt.Sprintf("a-%d-%d", i, seed)), floodMember(t, f, teams[1], fmt.Sprintf("b-%d-%d", i, seed))
		first := floodRun(t, f.submit(a, "build", fortyHex(1)), 201)
		second := floodRun(t, f.submit(b, "build", fortyHex(1)), 201)
		return first.RunID != second.RunID && f.submit(a, "build", fortyHex(2)).Code == 429 && f.submit(b, "build", fortyHex(2)).Code == 429
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 12}); err != nil {
		t.Fatal(err)
	}
}
