package controller

import (
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestClaimAttentionPreservesEnrollmentWhenARunnerIsOnline(t *testing.T) {
	accept, err := match.ParseAccept([]string{"github.com/acme/*"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	registered := []store.RegisteredAgent{{LastSeen: now, Name: "runner", CapabilityPrefix: "root", Profile: match.Profile{
		Name: "runner", Class: match.ClassAgent, Location: "local", ReservedPools: []string{"prod"},
		Accept: accept, Labels: []string{"gpu"}, OS: "linux", Arch: "arm64",
	}}}
	sighted := sightAgents(registered, now)
	if len(sighted) != 1 || !sighted[0].online {
		t.Fatalf("online agent: %+v", sighted)
	}
	p := sighted[0].Profile
	if p.Name != "runner" || p.Class != match.ClassAgent || p.Location != "local" || p.OS != "linux" || p.Arch != "arm64" || !slices.Equal(p.ReservedPools, []string{"prod"}) || !slices.Equal(p.Labels, []string{"gpu"}) {
		t.Fatalf("online observations replaced enrollment: %+v", p)
	}
	node := store.WaitingNode{NodeID: "job", Team: "a", Selector: []string{"pool=prod", "gpu"}, AllowedPools: []string{"prod"}, Repo: match.Repository{URL: "https://github.com/acme/repo"}}
	if reason := claimAttention(node, sighted, false); reason != "" {
		t.Fatalf("eligible reserved agent: %q", reason)
	}
	node.Repo.URL = "https://github.com/other/repo"
	if reason := claimAttention(node, sighted, false); reason == "" {
		t.Fatal("online observations removed repository acceptance")
	}
}
