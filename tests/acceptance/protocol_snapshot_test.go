package acceptance_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

type snapshotPipeline struct{}

func (snapshotPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, run sparkwing.RunContext) error {
	git, err := json.Marshal(run.Git)
	if err != nil {
		return err
	}
	pr, err := json.Marshal(run.Trigger.PullRequest)
	if err != nil {
		return err
	}
	sparkwing.Job(plan, "snapshot", func(context.Context) error { return nil }).
		Env("SNAPSHOT_GIT", string(git)).Env("SNAPSHOT_PR", string(pr))
	return nil
}

func init() {
	sparkwing.Register[sparkwing.NoInputs]("fake-host-run-snapshot", func() sparkwing.Pipeline[sparkwing.NoInputs] {
		return snapshotPipeline{}
	})
}

func testGoSDKFakeHostRunSnapshot(t *testing.T) {
	opts := sparkwingtest.Options{Run: sparkwing.RunContext{
		Git: sparkwing.NewGit("", "first-sha", "feature", "main", "owner/repo", "https://example.test/repo"),
		Trigger: sparkwing.TriggerInfo{PullRequest: &sparkwing.PullRequest{
			Number: 1, Action: "opened", BaseRef: "main", BaseSHA: "base-sha", HeadRef: "feature", HeadSHA: "first-sha",
		}},
	}}
	first := sparkwingtest.New(opts)
	t.Cleanup(func() { _ = first.Close() })
	want, err := first.Plan(t.Context(), "fake-host-run-snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	*opts.Run.Git = *sparkwing.NewGit("", "next-sha", "next", "trunk", "owner/next", "https://example.test/next")
	*opts.Run.Trigger.PullRequest = sparkwing.PullRequest{
		Number: 2, Action: "synchronize", BaseRef: "trunk", BaseSHA: "next-base", HeadRef: "next", HeadSHA: "next-sha",
	}
	second := sparkwingtest.New(opts)
	t.Cleanup(func() { _ = second.Close() })
	next, err := second.Plan(t.Context(), "fake-host-run-snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(next, want) {
		t.Fatal("second fixture did not receive its own run snapshot")
	}
	got, err := first.Plan(t.Context(), "fake-host-run-snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating options for the second fixture changed the first: got %+v, want %+v", got.Jobs[0].Env, want.Jobs[0].Env)
	}
}
