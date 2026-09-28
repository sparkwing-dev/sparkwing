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

func (s *Server) sweepClaimAttention(ctx context.Context) {
	now := time.Now()
	waiting, err := s.store.ListWaitingNodes(ctx, now.Add(-claimAttentionGrace))
	if err != nil {
		s.logger.Error("claim attention sweep failed", "err", err)
		return
	}
	online := s.runnerPresence.byToken(now, agentOnlineWithin)
	agents := map[store.Team][]agentSighting{}
	for _, n := range waiting {
		sightings, ok := agents[n.Team]
		if !ok {
			registered, err := s.store.ListRegisteredAgents(ctx, n.Team)
			if err != nil {
				s.logger.Error("claim attention sweep failed", "err", err)
				return
			}
			sightings = sightAgents(registered, online)
			agents[n.Team] = sightings
		}
		reason := claimAttention(n, sightings, s.Metering())
		if reason == n.Attention {
			continue
		}
		if err := s.store.SetNodeAttention(ctx, n.Team, n.RunID, n.NodeID, reason); err != nil {
			s.logger.Error("claim attention sweep failed", "err", err)
		}
	}
}

type agentSighting struct {
	store.RegisteredAgent
	profile match.Profile
	online  bool
}

// safety: an offline agent is judged on what it last advertised.
func sightAgents(registered []store.RegisteredAgent, online map[string]runnerPresence) []agentSighting {
	out := make([]agentSighting, 0, len(registered))
	for _, a := range registered {
		sighting := agentSighting{RegisteredAgent: a, profile: match.Profile{Name: a.Name, Class: match.ClassAgent, Labels: a.Labels}}
		if p, ok := online[a.TokenPrefix]; ok {
			sighting.online = true
			sighting.profile.Labels = append(match.SelfAsserted(p.Labels), a.Labels...)
			sighting.profile.OS, sighting.profile.Arch = p.profile.OS, p.profile.Arch
		}
		out = append(out, sighting)
	}
	return out
}

func claimAttention(n store.WaitingNode, agents []agentSighting, cloud bool) string {
	demand := match.Demand{Selector: n.Selector}
	var offline []agentSighting
	profiles := make([]match.Profile, 0, len(agents)+1)
	for _, a := range agents {
		profiles = append(profiles, a.profile)
		if match.Evaluate(a.profile, demand).OK() {
			if a.online {
				return ""
			}
			offline = append(offline, a)
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
