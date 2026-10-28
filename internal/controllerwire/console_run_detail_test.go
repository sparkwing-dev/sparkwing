package controllerwire

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

var updateConsoleFixtures = flag.Bool("update-console-fixtures", false, "rewrite the console's run detail fixtures from the Go type")

// The console's run page adapts these fixtures in web/src/lib/api.test.ts, so they must be exactly what the
// controller encodes: a renamed or added RunDetail field fails here until the fixtures are regenerated with
// -update-console-fixtures and the console adapter follows.
func TestConsoleRunDetailFixturesMatchTheWireType(t *testing.T) {
	started := time.Date(2026, 10, 28, 5, 1, 19, 0, time.UTC)
	finished := started.Add(33 * time.Second)
	inputs := runspec.Display{
		Pipeline: "hello", RequestedArgs: map[string]string{}, Args: map[string]string{"target": "prod"}, SecretArgs: []string{},
		Git:     runspec.Git{Repository: runspec.Repository{Key: "git:gitlab.com/acme/app", Display: "gitlab.com/acme/app"}, Ref: "main", SHA: "1b8e292f29420af1ae8e113e53faf3c05fb17249"},
		Lineage: runspec.Lineage{RetryOf: "run-earlier"},
	}
	fixtures := map[string]RunDetail{
		"run-detail-plan-failure.json": {
			ID: "run-plan", Number: 14, Team: "acme", Status: "failed", StateSeq: 3, Inputs: inputs,
			Failure: "install source entry: # pipeline\njobs/broken.go:6:9: undefined: qaDeliberateUndefined",
			Jobs:    []JobDetail{}, Attempts: []AttemptDetail{}, Notices: []engineid.Notice{}, FinishedAt: &finished,
		},
		"run-detail-jobs.json": {
			ID: "run-jobs", Number: 15, Team: "acme", Status: "failed", StateSeq: 9, Inputs: inputs,
			Jobs: []JobDetail{
				{ID: "build", Status: "done", Outcome: "failed", Error: "install source entry: ./pipeline.go:3:9: cannot use *** as int value", Generation: 1, Expansion: "none", FinishedAt: &finished},
				{ID: "test", Status: "done", Outcome: "success", Generation: 1, Attempt: 1, StateSeq: 4, Expansion: "none", StartedAt: &started, FinishedAt: &finished, Annotations: []Annotation{{Level: "notice", Message: "42 tests"}}},
			},
			Attempts:  []AttemptDetail{{JobID: "test", Attempt: 1, Generation: 1, Outcome: "success", Executor: "cloud", StartedAt: &started, FinishedAt: &finished}},
			Notices:   []engineid.Notice{},
			StartedAt: &started, FinishedAt: &finished,
		},
	}
	for name, detail := range fixtures {
		path := filepath.Join("..", "..", "web", "src", "lib", "testdata", name)
		want, err := json.MarshalIndent(detail, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, '\n')
		if *updateConsoleFixtures {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from the RunDetail the controller encodes; rerun with -update-console-fixtures and update web/src/lib/api.ts", name)
		}
	}
}
