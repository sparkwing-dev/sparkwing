//go:build unix

package nodehost

import (
	"errors"
	"maps"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

func TestRangeSkips(t *testing.T) {
	linear := []nodeproto.StepSpec{{ID: "a"}, {ID: "b", Needs: []string{"a"}}, {ID: "c", Needs: []string{"b"}}}
	branching := []nodeproto.StepSpec{{ID: "root"}, {ID: "left", Needs: []string{"root"}}, {ID: "right", Needs: []string{"root"}}, {ID: "join", Needs: []string{"left", "right"}}}
	for _, tc := range []struct {
		name        string
		steps       []nodeproto.StepSpec
		start, stop string
		want        map[string]string
	}{
		{"StartAt", linear, "b", "", map[string]string{"a": "upstream of --start-at=b"}},
		{"StopAt", linear, "", "b", map[string]string{"c": "downstream of --stop-at=b"}},
		{"StartEqualsStop", linear, "b", "b", map[string]string{"a": "outside --start-at=b --stop-at=b range", "c": "outside --start-at=b --stop-at=b range"}},
		{"BranchingStartAtSkipsAllButItsDescendants", branching, "left", "", map[string]string{"root": "upstream of --start-at=left", "right": "upstream of --start-at=left"}},
		{"NoBounds", linear, "", "", map[string]string{}},
		{"BoundInAnotherNode", linear, "elsewhere", "", map[string]string{}},
		{"OneBoundHere", linear, "elsewhere", "b", map[string]string{"c": "outside --start-at=elsewhere --stop-at=b range"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			children := map[string][]string{}
			for _, st := range tc.steps {
				for _, d := range st.Needs {
					children[d] = append(children[d], st.ID)
				}
			}
			got := rangeSkips(tc.steps, children, tc.start, tc.stop)
			if got == nil {
				got = map[string]string{}
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("skips = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateStepsRefusesAnUnorderableNode(t *testing.T) {
	for name, steps := range map[string][]nodeproto.StepSpec{
		"duplicate":    {{ID: "a"}, {ID: "a"}},
		"unknown need": {{ID: "a", Needs: []string{"ghost"}}},
		"cycle":        {{ID: "a", Needs: []string{"b"}}, {ID: "b", Needs: []string{"a"}}},
		"no id":        {{ID: ""}},
	} {
		err := validateNodes([]nodeproto.JobSpec{{ID: "n", Steps: steps}}, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}})
		if perr := (*apierr.Error)(nil); !errors.As(err, &perr) || perr.Code != apierr.PlanInvalid {
			t.Errorf("%s: err = %v, want plan_invalid", name, err)
		}
	}
	ok := []nodeproto.StepSpec{{ID: "a"}, {ID: "b", Needs: []string{"a"}}, {ID: "c", Needs: []string{"a", "b"}}}
	if err := validateNodes([]nodeproto.JobSpec{{ID: "n", Steps: ok}}, nodeproto.ValidationContext{Placement: nodeproto.PlacementLocal, Secrets: noV2Secrets{}}); err != nil {
		t.Errorf("a valid step graph: %v", err)
	}
}
