package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestQueueStateShowsOnlyTheCallersTeamsRunners(t *testing.T) {
	srv := New(nil, nil)
	now := time.Now()
	srv.WithTenantResolver(func(_ context.Context, team store.Team) (TenantHandle, error) {
		name, cores := "operator-box", 4.0
		if team == "team-b" {
			name, cores = "team-b-box", 2.0
		}
		return queueRunnerTenant{queueTeamReader: queueTeamReader{team: team}, runnerInventoryFake: &runnerInventoryFake{team: team, snapshot: capability.RunnerSnapshot{Now: now, Runners: []capability.RunnerObservation{{Team: string(team), Runner: nodeproto.RunnerIdentity{Name: name, Type: "runner"}, Headroom: &capability.RunnerHeadroom{Cores: cores, ObservedAt: now}}}}}}, nil
	})

	view := func(team store.Team) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil)
		rec := httptest.NewRecorder()
		if err := srv.handleQueueStateView(rec, req, capability.Authorized{Target: capability.Target{Team: string(team)}}); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("queue state for %s = %d: %s", team, rec.Code, rec.Body)
		}
		var qs wingwire.QueueState
		if err := json.NewDecoder(rec.Body).Decode(&qs); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, r := range qs.Runners {
			names = append(names, r.Name)
		}
		return names
	}
	if got := view("team-b"); len(got) != 1 || got[0] != "team-b-box" {
		t.Errorf("team B sees runners %v, want only its own", got)
	}
	if got := view(store.DefaultTeam); len(got) != 1 || got[0] != "operator-box" {
		t.Errorf("the operator's team sees runners %v, want only its own", got)
	}
}

type queueTeamReader struct{ team store.Team }

func (f queueTeamReader) Team() store.Team { return f.team }

func (queueTeamReader) ListConcurrencyStates(context.Context) ([]*store.ConcurrencyState, error) {
	return nil, nil
}

type queueRunnerTenant struct {
	queueTeamReader
	*runnerInventoryFake
}

func (q queueRunnerTenant) Team() store.Team { return q.queueTeamReader.Team() }

func TestQueueStateCarriesTheLocalAdmissionDaemon(t *testing.T) {
	srv := New(nil, nil)
	srv.WithTenantResolver(func(_ context.Context, team store.Team) (TenantHandle, error) {
		return queueRunnerTenant{queueTeamReader: queueTeamReader{team: team}, runnerInventoryFake: &runnerInventoryFake{team: team, snapshot: capability.RunnerSnapshot{Now: time.Now()}}}, nil
	})
	view := func() *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		err := srv.handleQueueStateView(rec, httptest.NewRequest(http.MethodGet, "/api/v1/queue", nil), capability.Authorized{Target: capability.Target{Team: string(store.DefaultTeam)}})
		if err != nil {
			routepolicy.WritePolicyError(rec, err)
		}
		return rec
	}
	srv.WithLocalAdmission(func(context.Context) (wingwire.QueueState, error) {
		return wingwire.QueueState{DaemonVersion: "v1.2.3", Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 8, Held: 2}}}, nil
	})
	rec := view()
	var qs wingwire.QueueState
	if err := json.NewDecoder(rec.Body).Decode(&qs); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %v", rec.Code, err)
	}
	if qs.DaemonVersion != "v1.2.3" || len(qs.Resources) != 1 || qs.Resources[0].Key != "cores" {
		t.Fatalf("queue state %+v lost the daemon's ledger", qs)
	}
	srv.WithLocalAdmission(func(context.Context) (wingwire.QueueState, error) {
		return wingwire.QueueState{}, errors.New("daemon wedged")
	})
	if rec := view(); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unanswering daemon gave HTTP %d, want 503: %s", rec.Code, rec.Body)
	}
}
