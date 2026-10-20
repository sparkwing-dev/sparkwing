package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

const (
	// safety: unclaimed work is judged only after it has waited this long.
	claimAttentionGrace = 30 * time.Second
	agentOnlineWithin   = 2 * time.Minute
)

// perf: a tick judges at most this many nodes and resumes after the last one
// next tick, so a large backlog costs bounded work per tick.
const claimAttentionBatch = 1000

// safety: a pass reads only nodes ready before it began, so nodes that arrive
// behind its cursor cannot keep it from wrapping, and every waiting node is
// judged within ceil(N/batch) ticks.
func (s *Server) sweepClaimAttention(ctx context.Context, now time.Time, batch int) {
	if s.attentionCursor == ([2]string{}) {
		s.attentionPassAt = now
	}
	waiting, err := s.operator.ListWaitingNodes(ctx, s.attentionPassAt.Add(-claimAttentionGrace), s.attentionCursor, batch)
	if err != nil {
		s.logger.Error("claim attention sweep failed", "err", err)
		return
	}
	s.attentionCursor = [2]string{}
	if len(waiting) == batch {
		last := waiting[len(waiting)-1]
		s.attentionCursor = [2]string{last.RunID, last.NodeID}
	}
	teams := make([]store.Team, 0, len(waiting))
	for _, n := range waiting {
		teams = append(teams, n.Team)
	}
	var updates []store.NodeAttention
	if err := s.operator.EachTeam(ctx, teams, func(tenant *store.Tenant) error {
		registered, err := tenant.ListRegisteredAgents(ctx)
		if err != nil {
			return err
		}
		sightings := sightAgents(registered, now)
		for _, n := range waiting {
			if n.Team != tenant.Team() {
				continue
			}
			if reason := claimAttention(n, sightings, s.deployment.Metering()); reason != n.Attention {
				updates = append(updates, store.NodeAttention{Team: n.Team, RunID: n.RunID, NodeID: n.NodeID, Reason: reason})
			}
		}
		return nil
	}); err != nil {
		s.logger.Error("claim attention sweep failed", "err", err)
		return
	}
	if err := s.operator.SetNodeAttention(ctx, updates); err != nil {
		s.logger.Error("claim attention sweep failed", "err", err)
	}
}

type agentSighting struct {
	store.RegisteredAgent
	online bool
}

func sightAgents(registered []store.RegisteredAgent, now time.Time) []agentSighting {
	out := make([]agentSighting, 0, len(registered))
	for _, a := range registered {
		online := !a.LastSeen.IsZero() && !a.LastSeen.After(now) && now.Sub(a.LastSeen) <= agentOnlineWithin
		out = append(out, agentSighting{RegisteredAgent: a, online: online})
	}
	return out
}

func claimAttention(n store.WaitingNode, agents []agentSighting, cloud bool) string {
	repo := n.Repo
	demand := match.Demand{Selector: n.Selector, Repo: &repo, Request: n.Request, AllowedPools: n.AllowedPools}
	var offline []agentSighting
	var refused []string
	profiles := make([]match.Profile, 0, len(agents)+1)
	for _, a := range agents {
		profiles = append(profiles, a.Profile)
		verdict := match.Evaluate(a.Profile, demand)
		switch {
		case verdict.OK() && a.online:
			return ""
		case verdict.OK():
			offline = append(offline, a)
		case verdict.Reason == match.ReasonRepo || verdict.Reason == match.ReasonShape:
			refused = append(refused, a.Name+" ("+string(verdict.Reason)+")")
		}
	}
	if cloud {
		runner := match.Profile{Class: match.ClassCloud, Location: "cloud", Labels: match.ToolLabels(match.CloudTools)}
		if match.Evaluate(runner, demand).OK() {
			return ""
		}
		profiles = append(profiles, runner)
	}
	needs := "node " + n.NodeID + " needs " + strings.Join(n.Selector, ", ")
	if len(n.Selector) == 0 {
		needs = "node " + n.NodeID + " needs an agent"
	}
	if len(offline) > 0 {
		sort.SliceStable(offline, func(i, j int) bool { return offline[i].LastSeen.After(offline[j].LastSeen) })
		seen := "never seen"
		if !offline[0].LastSeen.IsZero() {
			seen = "last seen " + offline[0].LastSeen.UTC().Format(time.RFC3339)
		}
		reason := fmt.Sprintf("%s; eligible agent %s is offline (%s)", needs, offline[0].Name, seen)
		if len(offline) > 1 {
			reason += fmt.Sprintf(", as are %d more", len(offline)-1)
		}
		return reason
	}
	if len(refused) > 0 {
		return fmt.Sprintf("%s; every agent in team %s that matches refuses it: %s", needs, n.Team, strings.Join(refused, ", "))
	}
	unmet, it := unmetTerms(n.Selector, profiles), "it"
	switch {
	case len(n.Selector) == 0:
		return fmt.Sprintf("%s; team %s has no agent", needs, n.Team)
	case len(unmet) == 0:
		it = "all of them"
	case len(unmet) > 1:
		it = "them"
	}
	if len(unmet) > 0 {
		needs = "node " + n.NodeID + " needs " + strings.Join(unmet, ", ")
	}
	reason := fmt.Sprintf("%s; no agent in team %s has %s", needs, n.Team, it)
	if cloud {
		reason += " and Sparkwing Cloud runners don't provide " + it
	}
	return reason
}

func unmetTerms(selector []string, profiles []match.Profile) []string {
	var out []string
	for _, term := range selector {
		met := false
		for _, p := range profiles {
			met = met || match.TermSatisfied(p, term)
		}
		if !met {
			out = append(out, term)
		}
	}
	return out
}
