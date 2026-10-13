package nodehost

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestChildLeaseIsThePinnedRunsLease(t *testing.T) {
	st := openStore(t)
	doc := nodeproto.PlanDoc{Resources: &nodeproto.PlanResources{Cores: 1}, Nodes: grouped("box", "queue", 1, 0, "parent")}
	var env []string
	exec := func(_ context.Context, n *NodeRun) Result {
		env = hostEnv(n)
		return Result{Outcome: sparkwing.Success}
	}
	res := runAdmitted(t, t.Context(), st, doc, &Admission{Daemon: newFakeDaemon()}, exec)
	if res.Status != "success" {
		t.Fatal(res)
	}
	if len(env) != 1 || env[0] != "SPARKWING_LEASE_TOKEN=tok" {
		t.Fatalf("nested CLI gets %s instead of the pinned run lease: %s", strings.Join(env, ","), "SPARKWING_LEASE_TOKEN=tok")
	}
}

func TestChildLeaseFollowsTheDispatchersRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  *Run
		want string
	}{
		{"run holds plan groups", &Run{leaseToken: "run", leaseHoldsGroups: true}, "run"},
		{"run pins the host", &Run{leaseToken: "run", Doc: nodeproto.PlanDoc{Resources: &nodeproto.PlanResources{Cores: 1}}}, "run"},
		{"node holds its own cores", &Run{leaseToken: "run"}, "node"},
	} {
		if got := childLease(&NodeRun{Run: tc.run, nodeLease: "node"}); got != tc.want {
			t.Errorf("%s: child attaches to %q, want %q", tc.name, got, tc.want)
		}
	}
}
