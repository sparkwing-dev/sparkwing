package controller

import (
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/match"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestClaimAttentionPreservesEnrollmentWhenARunnerIsOnline(t *testing.T) {
	accept, err := match.ParseAccept([]string{"github.com/acme/*"})
	if err != nil {
		t.Fatal(err)
	}
	registered := []store.RegisteredAgent{{Name: "runner", CapabilityPrefix: "root", Profile: match.Profile{
		Name: "runner", Class: match.ClassAgent, Location: "local", ReservedPools: []string{"prod"},
		Capacity: match.Resources{Cores: 4}, Accept: accept,
	}}}
	online := map[string]runnerPresence{"root": {
		Labels:  []string{"gpu", "pool=forged"},
		profile: match.Profile{Name: "impostor", Class: match.ClassCloud, Location: "cloud", ReservedPools: []string{"forged"}, Capacity: match.Resources{Cores: 100}, OS: "linux", Arch: "arm64"},
	}}
	sighted := sightAgents(registered, online, time.Now())
	if len(sighted) != 1 || !sighted[0].online {
		t.Fatalf("online agent: %+v", sighted)
	}
	p := sighted[0].Profile
	if p.Name != "runner" || p.Class != match.ClassAgent || p.Location != "local" || p.Capacity.Cores != 4 || p.OS != "linux" || p.Arch != "arm64" || !slices.Equal(p.ReservedPools, []string{"prod"}) || !slices.Equal(p.Labels, []string{"gpu"}) {
		t.Fatalf("online observations replaced enrollment: %+v", p)
	}
	node := store.WaitingNode{NodeID: "job", Team: "a", Selector: []string{"pool=prod", "gpu"}, AllowedPools: []string{"prod"}, Repo: match.Repository{URL: "https://github.com/acme/repo"}, Request: match.Resources{Cores: 4}}
	if reason := claimAttention(node, sighted, false); reason != "" {
		t.Fatalf("eligible reserved agent: %q", reason)
	}
	node.Request.Cores = 5
	if reason := claimAttention(node, sighted, false); reason == "" {
		t.Fatal("reported capacity replaced enrolled capacity")
	}
	node.Request.Cores, node.Repo.URL = 4, "https://github.com/other/repo"
	if reason := claimAttention(node, sighted, false); reason == "" {
		t.Fatal("online observations removed repository acceptance")
	}
}
