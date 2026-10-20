package sparkwing

import (
	"context"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestChildTargetsComposeRepositories(t *testing.T) {
	plan := NewPlan()
	Job(plan, "release", func(context.Context) error { return nil }).
		StartsChildren("deploy").StartsChildrenIn("gh:123", "publish").StartsChildren("smoke")
	got := planDocument(plan).Jobs[0].Children
	want := []nodeproto.ChildPipeline{{Pipeline: "deploy"}, {Pipeline: "publish", Repository: "gh:123"}, {Pipeline: "smoke"}}
	if !slices.Equal(got, want) {
		t.Fatalf("child targets = %+v, want %+v", got, want)
	}
}
