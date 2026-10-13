package orchestrator

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestHostedChildrenQueueOnTheParentsLease(t *testing.T) {
	ctx := t.Context()
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateRun(ctx, store.Run{ID: "parent", Pipeline: "release", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNode(ctx, store.Node{RunID: "parent", NodeID: "gate", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	masker := secrets.NewMasker()
	masker.Register("hunter2")
	kids := hostedChildren{state: localState{st: st}, masker: masker, leases: &sync.Map{}}
	parent := nodehost.ChildParent{RunID: "parent", NodeID: "gate", LeaseToken: "lease-1"}

	id, err := kids.StartChild(ctx, parent, nodehost.ChildRequest{Pipeline: "build", Args: map[string]string{"token": "hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	trig, err := st.GetTrigger(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if trig.ParentRunID != "parent" || trig.ParentNodeID != "gate" || len(trig.TriggerEnv) != 0 {
		t.Errorf("trigger parent=%s/%s env=%v: the lease must stay out of the stored trigger", trig.ParentRunID, trig.ParentNodeID, trig.TriggerEnv)
	}
	if env := kids.childEnv(trig); !slices.Contains(env, wingwire.ChildLeaseTokenEnv+"=lease-1") {
		t.Errorf("child env = %v: the child must attach to the parent's lease", env)
	}
	events, err := st.ListEventsAfter(ctx, "parent", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var start string
	for _, ev := range events {
		if ev.Kind == "child_run_start" {
			start = string(ev.Payload)
		}
	}
	if start == "" || !strings.Contains(start, id) || strings.Contains(start, "hunter2") {
		t.Errorf("child_run_start = %q, want the child id and masked args", start)
	}

	if _, err := kids.StartChild(ctx, parent, nodehost.ChildRequest{Pipeline: "release"}); !errors.Is(err, nodehost.ErrChildCycle) {
		t.Errorf("re-entering the parent's pipeline: err=%v, want ErrChildCycle", err)
	}
}
