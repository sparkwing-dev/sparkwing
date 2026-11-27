package jobs

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func TestHeavyGateAndLandJobsTakeTheMachineSuiteSlotOneAfterAnother(t *testing.T) {
	queued := map[string]string{"queued": "true", "queued-main": strings.Repeat("a", 40), "brief": "Review the suite slot"}
	for _, c := range []struct {
		name     string
		pipeline string
		args     map[string]string
		members  []string
	}{
		{"gate", "Gate", nil, []string{"gate"}},
		{"ordinary land", "Land", nil, []string{"gate"}},
		{"queued land", "Land", queued, []string{"fast", "affected-tests"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw, err := newTestHost(t, sparkwingtest.Options{Run: sparkwing.RunContext{Pipeline: strings.ToLower(c.pipeline)}}).Plan(t.Context(), c.pipeline, c.args)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Jobs []struct {
					ID          string
					Needs       []string `json:"deps"`
					Concurrency []struct {
						Group    string
						Scope    *string
						Capacity *int
						OnLimit  *string `json:"on_limit"`
					}
				}
			}
			if err := json.Unmarshal(encoded, &doc); err != nil {
				t.Fatal(err)
			}
			var members []string
			needs := map[string][]string{}
			for _, job := range doc.Jobs {
				needs[job.ID] = job.Needs
				for _, group := range job.Concurrency {
					if group.Group != "machine-suite" {
						continue
					}
					if group.Scope == nil || *group.Scope != "box" || group.Capacity == nil || *group.Capacity != 1 || group.OnLimit != nil && *group.OnLimit != "queue" {
						t.Fatalf("job %s must queue for one box-wide suite slot: %s", job.ID, encoded)
					}
					members = append(members, job.ID)
				}
			}
			if !slices.Equal(members, c.members) {
				t.Fatalf("machine-suite members = %v, want %v", members, c.members)
			}
			// safety: a member that can start beside a sibling member would queue behind its own run.
			for i := 1; i < len(members); i++ {
				if !slices.Contains(needs[members[i]], members[i-1]) {
					t.Fatalf("%s can start beside %s and would queue behind its own run: needs %v", members[i], members[i-1], needs[members[i]])
				}
			}
		})
	}
}
