package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func (s *EngineController) unclaimedNotice(ctx context.Context, view store.RunView) (*engine.Notice, error) {
	unit, run := view.Waiting, view.Snapshot.Run
	if unit == nil || s.Store == nil {
		return nil, nil
	}
	switch store.RequestPlacement(run.Request.Request) {
	case nodeproto.PlacementRunnerHosted, nodeproto.PlacementLauncher, nodeproto.PlacementGitHubActions:
	default:
		return nil, nil
	}
	waited := view.Snapshot.StoreNow.Sub(unit.Since)
	if waited < claimAttentionGrace {
		return nil, nil
	}
	selector := append([]string{}, unit.Needs...)
	switch runner := run.Request.Request.Options.Runner.Value; runner.Kind {
	case runspec.RunnerName:
		selector = append(selector, "name="+runner.Value)
	case runspec.RunnerLabel:
		selector = append(selector, runner.Value)
	}
	hint := "Sparkwing Cloud's launcher takes this run's plan, evaluation and job units; check that the launcher is running and can place work for team " + string(run.Team)
	if run.Dispatch != string(runspec.DispatchController) {
		online, err := s.eligibleAgentOnline(ctx, run, unit, selector, view.Snapshot.StoreNow)
		if online || err != nil {
			return nil, err
		}
		hint = "a self-hosted runner that matches must poll for it"
		if len(selector) > 0 {
			hint += " (needs " + strings.Join(selector, ", ") + ")"
		}
	}
	what := map[capability.UnitKind]string{capability.UnitTrigger: "the run's trigger", capability.UnitPlan: "the run's plan", capability.UnitEval: "evaluation of job " + unit.NodeID, capability.UnitNode: "job " + unit.NodeID}[unit.Kind]
	return &engine.Notice{Kind: engine.NoticeUnclaimed, Reason: fmt.Sprintf("no eligible runner or launcher for %s after %s", what, waited.Round(time.Second)), Hint: hint}, nil
}

func (s *EngineController) eligibleAgentOnline(ctx context.Context, run store.RunRow, unit *store.UnclaimedUnit, selector []string, now time.Time) (bool, error) {
	tenant, err := s.Store.ForTeam(ctx, run.Team)
	if err != nil {
		return false, err
	}
	registered, err := tenant.ListRegisteredAgents(ctx)
	if err != nil {
		return false, err
	}
	repo := match.Repository{URL: run.Request.Request.Git.Repository.Display}
	demand := match.Demand{Selector: selector, Repo: &repo, AllowedPools: run.AllowedPools, Trigger: unit.Kind == capability.UnitTrigger}
	for _, agent := range sightAgents(registered, now) {
		if agent.online && match.Evaluate(agent.Profile, demand).OK() {
			return true, nil
		}
	}
	return false, nil
}
