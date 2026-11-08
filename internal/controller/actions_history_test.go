package controller

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func assertDispatchHistory(t *testing.T, f *dispatchFixture, wants ...string) {
	t.Helper()
	detail, err := f.e.RunDetail(t.Context(), capability.Authorized{Target: capability.Target{Team: "a", RunID: "run"}})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, notice := range detail.Notices {
		if notice.Kind == engineid.NoticePlacementHold {
			text += notice.Reason + " " + notice.Hint + "\n"
		}
	}
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("dispatch history omits %q: %s", want, text)
		}
	}
}

func TestActionsHistoryRetainsEveryGenerationAfterRunFinishes(t *testing.T) {
	f := newDispatchFixture(t)
	d := f.dispatch(t)
	wants := []string{"5 dispatches", d.Receipt, "accepted"}
	for i, state := range []string{"requested", "refused", "uncertain", "cancelled"} {
		receipt := fmt.Sprintf("receipt-%d", i)
		engineSQL(t, f.e.Store, `INSERT INTO github_actions_dispatches(receipt,team,run_id,node_id,queue_started_at,generation,approval_id,installation_id,repository_id,repository_owner_id,repository,workflow,workflow_ref,runner,policy_revision,state,reason) VALUES(?,'a','run','job',1,?,'approval',7,1,7,'acme/repo',?,'refs/heads/sparkwing-fallback','ubuntu-latest','',?,?)`, receipt, i+2, fallbackWorkflow, state, "reason-"+state)
		wants = append(wants, receipt, state, "reason-"+state)
	}
	assertDispatchHistory(t, f, wants...)
	engineSQL(t, f.e.Store, `UPDATE nodes SET status='done',outcome='success',ready_at=NULL,queue_started_at=NULL,finished_at=? WHERE node_id='job'`, f.now.UnixNano())
	engineSQL(t, f.e.Store, `UPDATE runs SET status='success',finished_at=? WHERE id='run'`, f.now.UnixNano())
	assertDispatchHistory(t, f, wants...)
}
