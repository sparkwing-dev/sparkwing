package controller_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestAgents_OnlyShowExecutorsThatCanClaimForTheCallerTeam(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		ctx := t.Context()
		now := time.Now()
		type enrolled struct {
			name     string
			tenant   *store.Tenant
			claimant store.ClaimIdentity
		}
		var agents []enrolled
		for _, entry := range []struct {
			name   string
			tenant *store.Tenant
		}{
			{"private-default-host", forTeam(t, f.st, store.DefaultTeam)},
			{"private-team-a-host", f.teamA},
			{"private-team-b-host", f.teamB},
		} {
			_, token, err := entry.tenant.CreateToken(ctx, entry.name, store.TokenKindRunner,
				[]string{controller.ScopeNodesClaim}, 0, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.st.EnrollExecutor(ctx, token.Prefix, store.Executor{
				Name: entry.name, Principal: entry.name, Kind: "agent", Location: "local",
				MaxConcurrent: 2, Budget: store.ExecutorResource{Cores: 4},
			}); err != nil {
				t.Fatal(err)
			}
			claimant := store.ClaimIdentity{Principal: entry.name, TokenPrefix: token.Prefix}
			if err := f.st.HeartbeatExecutor(ctx, claimant, entry.name,
				store.ExecutorResource{Cores: 2}, 3, now); err != nil {
				t.Fatal(err)
			}
			agents = append(agents, enrolled{name: entry.name, tenant: entry.tenant, claimant: claimant})
		}

		seedRun(t, f.teamB, "busy-team-b", "build-b")
		if err := f.st.CreateNode(ctx, store.Node{RunID: "busy-team-b", NodeID: "work", Status: "pending"}); err != nil {
			t.Fatal(err)
		}
		if err := f.st.MarkNodeReady(ctx, "busy-team-b", "work"); err != nil {
			t.Fatal(err)
		}
		summary, err := f.st.SchedulingSummary(ctx, "busy-team-b", "work")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.ClaimReadyNodeForExecutorWithReservation(ctx, agents[2].claimant,
			agents[2].name, "busy-team-b", "work", "runner:team-b:0", time.Minute,
			"reservation-team-b", 0, summary.ResourceDigest); err != nil {
			t.Fatal(err)
		}

		for _, caller := range []struct {
			name  string
			auth  string
			want  string
			slots int
		}{
			{"team A owner", f.ownerA, agents[1].name, 0},
			{"team B owner", f.ownerB, agents[2].name, 1},
		} {
			code, body := f.do(http.MethodGet, "/api/v1/agents", caller.auth, nil)
			if code != http.StatusOK {
				t.Fatalf("%s agents = %d: %s", caller.name, code, body)
			}
			var response struct {
				Agents []controller.Agent `json:"agents"`
			}
			if err := json.Unmarshal([]byte(body), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Agents) != 1 || response.Agents[0].Name != caller.want ||
				response.Agents[0].ActiveSlots == nil || *response.Agents[0].ActiveSlots != caller.slots ||
				response.Agents[0].Headroom == nil || response.Agents[0].Headroom.QueueDepth != 3 {
				t.Fatalf("%s agents = %+v, want only %s with %d slots", caller.name, response.Agents, caller.want, caller.slots)
			}
		}
	})
}
