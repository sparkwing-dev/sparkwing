package nodehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// The dispatcher's claimed path waits on the parent alone: a recovery node's
// own deps neither hold it back nor cancel it.
func TestRecoveryWaitsOnItsParentAlone(t *testing.T) {
	st := openStore(t)
	parentIn, release := make(chan struct{}), make(chan struct{})
	fx := &fakeExec{bodies: map[string]Executor{
		"slow": func(ctx context.Context, _ *NodeRun) Result {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return Result{Outcome: sparkwing.Failed, Err: errors.New("slow broke")}
		},
		"deploy": func(context.Context, *NodeRun) Result {
			close(parentIn)
			return Result{Outcome: sparkwing.Failed, Err: errors.New("deploy broke")}
		},
		"rollback": func(context.Context, *NodeRun) Result {
			// safety: slow is still running, so the recovery did not wait on its dep.
			close(release)
			return Result{Outcome: sparkwing.Success}
		},
	}}
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "slow", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "deploy", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "rollback", OnFailureOf: "deploy", Needs: deps("slow"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	hostDoc(t, ctx, st, doc, fx.run)

	<-parentIn
	wantRow(t, st, "rollback", sparkwing.Success, "")
	if f := fx.seen["rollback"].Failure; f == nil || f.Job != "deploy" || f.Stage != "action" || f.Error == nil || f.Error.Message != "deploy broke" {
		t.Errorf("rollback failure = %+v, want deploy's action failure", f)
	}
}

func TestRecoverySkipsAParentCancelledUpstream(t *testing.T) {
	st := openStore(t)
	fx := &fakeExec{bodies: map[string]Executor{"build": func(context.Context, *NodeRun) Result {
		return Result{Outcome: sparkwing.Failed, Err: errors.New("broke")}
	}}}
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "build", Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "deploy", Needs: deps("build"), Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "rollback", OnFailureOf: "deploy", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}

	hostDoc(t, t.Context(), st, doc, fx.run)

	wantRow(t, st, "rollback", sparkwing.Skipped, `parent "deploy" did not fail (outcome=cancelled)`)
	if fx.ran("rollback") {
		t.Error("the recovery ran for a cancelled parent")
	}
}

func TestAFailedRecoveryLeavesTheRunToItsParent(t *testing.T) {
	st := openStore(t)
	broke := func(context.Context, *NodeRun) Result {
		return Result{Outcome: sparkwing.Failed, Err: errors.New("broke")}
	}
	fx := &fakeExec{bodies: map[string]Executor{"notify": broke, "notify-recover": broke}}
	doc := nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{
		{ID: "notify", Optional: true, Steps: []nodeproto.StepSpec{{ID: "body"}}},
		{ID: "notify-recover", OnFailureOf: "notify", Steps: []nodeproto.StepSpec{{ID: "body"}}},
	}}

	res := hostDoc(t, t.Context(), st, doc, fx.run)

	wantRow(t, st, "notify-recover", sparkwing.Failed, "broke")
	if res.Status != "success" {
		t.Errorf("run = %s %v, want success: neither an optional parent nor its recovery decides the run", res.Status, res.Err)
	}
}
