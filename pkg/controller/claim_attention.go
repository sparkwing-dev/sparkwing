package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	// safety: a node is only asked about once an idle agent would have taken
	// it, so one about to be claimed is never flagged.
	claimAttentionGrace = 30 * time.Second
	agentOnlineWithin   = 2 * time.Minute
)

// safety: the platform is stored as os= and arch= labels, so an offline agent
// is matched on the platform it last reported.
func (s *Server) recordAgentLabels(ctx context.Context, tokenPrefix string, labels []string, profile match.Profile) {
	labels = append([]string(nil), labels...)
	if profile.OS != "" && profile.Arch != "" {
		labels = append(labels, "os="+profile.OS, "arch="+profile.Arch)
	}
	if err := s.store.RecordAgentLabels(ctx, tokenPrefix, labels); err != nil {
		s.logger.Warn("recording an agent's advertised labels failed", "err", err)
	}
}

// perf: a tick judges at most this many nodes and resumes after the last one
// next tick, so a large backlog costs bounded work per tick.
var claimAttentionBatch = 1000

// safety: a pass reads only nodes ready before it began, so nodes that arrive
// behind its cursor cannot keep it from wrapping, and every waiting node is
// judged within ceil(N/claimAttentionBatch) ticks.
func (s *Server) sweepClaimAttention(ctx context.Context, now time.Time) {
	if s.attentionCursor == ([2]string{}) {
		s.attentionPassAt = now
	}
	waiting, err := s.store.ListWaitingNodes(ctx, s.attentionPassAt.Add(-claimAttentionGrace), s.attentionCursor, claimAttentionBatch)
	if err != nil {
		s.logger.Error("claim attention sweep failed", "err", err)
		return
	}
	s.attentionCursor = [2]string{}
	if len(waiting) == claimAttentionBatch {
		last := waiting[len(waiting)-1]
		s.attentionCursor = [2]string{last.RunID, last.NodeID}
	}
	online := s.runnerPresence.byToken(now, agentOnlineWithin)
	agents := map[store.Team][]agentSighting{}
	var updates []store.NodeAttention
	for _, n := range waiting {
		sightings, ok := agents[n.Team]
		if !ok {
			registered, err := s.store.ListRegisteredAgents(ctx, n.Team)
			if err != nil {
				s.logger.Error("claim attention sweep failed", "err", err)
				return
			}
			sightings = sightAgents(registered, online, now)
			agents[n.Team] = sightings
		}
		if reason := claimAttention(n, sightings, s.Metering()); reason != n.Attention {
			updates = append(updates, store.NodeAttention{Team: n.Team, RunID: n.RunID, NodeID: n.NodeID, Reason: reason})
		}
	}
	if err := s.store.SetNodeAttention(ctx, updates); err != nil {
		s.logger.Error("claim attention sweep failed", "err", err)
	}
}

type agentSighting struct {
	store.RegisteredAgent
	online bool
}

// safety: an enrolled executor is judged on its enrolled profile and its
// heartbeat, as its offers are; any other agent on what it sends when online
// and on what it last advertised when not.
func sightAgents(registered []store.RegisteredAgent, online map[string]runnerPresence, now time.Time) []agentSighting {
	out := make([]agentSighting, 0, len(registered))
	for _, a := range registered {
		sighting := agentSighting{RegisteredAgent: a}
		if a.Enrolled {
			sighting.online = now.Sub(a.LastSeen) <= store.ExecutorRegistrationActiveWindow
		} else if p, ok := online[a.TokenPrefix]; ok {
			sighting.online = true
			profile := p.profile
			profile.Name, profile.Class, profile.Labels = a.Name, match.ClassAgent, match.SelfAsserted(p.Labels)
			sighting.Profile = profile
		}
		out = append(out, sighting)
	}
	return out
}

func claimAttention(n store.WaitingNode, agents []agentSighting, cloud bool) string {
	repo := n.Repo
	demand := match.Demand{Selector: n.Selector, Repo: &repo, Request: n.Request}
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
