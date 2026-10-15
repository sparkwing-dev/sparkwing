package nodehost

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

const testRunID = "run-sched"

func deps(ids ...string) []string { return ids }

func TestValidateNodesRefusesAnInvalidPlan(t *testing.T) {
	for name, tc := range map[string]struct {
		nodes []nodeproto.JobSpec
		want  string
	}{
		"cycle": {
			nodes: []nodeproto.JobSpec{{ID: "a", Needs: deps("c"), Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "b", Needs: deps("a"), Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "c", Needs: deps("b"), Steps: []nodeproto.StepSpec{{ID: "body"}}}},
			want:  "dependency cycle",
		},
		"missing dep":      {nodes: []nodeproto.JobSpec{{ID: "a", Needs: deps("ghost"), Steps: []nodeproto.StepSpec{{ID: "body"}}}}, want: "job dependency is absent"},
		"duplicate node":   {nodes: []nodeproto.JobSpec{{ID: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}}}, want: "duplicate job ID"},
		"no id":            {nodes: []nodeproto.JobSpec{{Steps: []nodeproto.StepSpec{{ID: "body"}}}}, want: "identifier is empty"},
		"missing recovery": {nodes: []nodeproto.JobSpec{{ID: "r", OnFailureOf: "ghost", Steps: []nodeproto.StepSpec{{ID: "body"}}}}, want: "recovery source is absent"},
		"optional dep cycle": {
			nodes: []nodeproto.JobSpec{{ID: "a", NeedsIfPresent: deps("b"), Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "b", Needs: deps("a"), Steps: []nodeproto.StepSpec{{ID: "body"}}}},
			want:  "dependency cycle",
		},
		"duplicate closure": {
			nodes: []nodeproto.JobSpec{
				{ID: "a", Closures: nodeproto.ClosureSpec{SkipIf: []string{"x"}}, Steps: []nodeproto.StepSpec{{ID: "body"}}},
				{ID: "b", Steps: []nodeproto.StepSpec{{ID: "s", Closures: nodeproto.ClosureSpec{Verify: "x"}}}},
			},
			want: "closure ID is not unique",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateNodes(tc.nodes, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}})
			var perr *apierr.Error
			if !errors.As(err, &perr) || perr.Code != apierr.PlanInvalid || !strings.Contains(perr.Message, tc.want) {
				t.Fatalf("err = %v, want plan_invalid naming %q", err, tc.want)
			}
		})
	}
	if err := validateNodes([]nodeproto.JobSpec{{ID: "a", NeedsIfPresent: deps("absent"), Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "r", OnFailureOf: "a", Steps: []nodeproto.StepSpec{{ID: "body"}}}}, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}); err != nil {
		t.Errorf("a plan with an absent optional dependency and a recovery node: %v", err)
	}
}

func TestBoundedNodeErrTextKeepsUTF8AndShortText(t *testing.T) {
	if got := boundedNodeErrText("short"); got != "short" {
		t.Errorf("short text = %q, want it unchanged", got)
	}
	got := boundedNodeErrText(strings.Repeat("é", nodeErrTextMaxBytes))
	if !utf8.ValidString(got) || len(got) > nodeErrTextMaxBytes {
		t.Errorf("bounded text is %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}
