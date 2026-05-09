package orchestrator

// `wing X --explain --skip Y -o json` must produce a Plan snapshot
// identical (modulo formatting) to `wing X --explain --skip Y`. The
// bug was that explain-output flags (-o / --output / --json) were
// forwarded into parseTypedFlags, which rejected them as unknown and
// silently dropped *every* parsed flag (including --skip / --only)
// into an empty argsMap. The Plan was then built without any
// SkipFilter applied -- diverging from the no-`-o` invocation, where
// parsing succeeded and SkipFilter ran.

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// explainSkipInputs mirrors the embedded SkipFilterArgs pattern used by
// the platform's release pipelines: --skip and --only become
// first-class typed flags on the pipeline. The test pipeline below
// consults Skip in its own Plan() to drop a named node, mimicking
// how SkipFilter actually reshapes the DAG at Plan-construction
// time.
type explainSkipInputs struct {
	Skip string `flag:"skip" desc:"comma-separated step names to skip"`
}

type explainSkipPipe struct{}

func (explainSkipPipe) Plan(_ context.Context, plan *sparkwing.Plan, in explainSkipInputs, _ sparkwing.RunContext) error {
	skip := map[string]struct{}{}
	for _, s := range strings.Split(in.Skip, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			skip[s] = struct{}{}
		}
	}
	register := func(name string) {
		if _, dropped := skip[name]; dropped {
			return
		}
		sparkwing.Job(plan, name, func(ctx context.Context) error { return nil })
	}
	register("build")
	register("artifact")
	register("publish")
	return nil
}

func init() {
	sparkwing.Register[explainSkipInputs]("explain-skip-test", func() sparkwing.Pipeline[explainSkipInputs] {
		return explainSkipPipe{}
	})
}

// captureExplainStdout runs printPipelinePlan with the given args
// and returns its stdout bytes. printPipelinePlan writes directly
// to os.Stdout (it's a CLI entrypoint), so the test redirects the
// real fd through a pipe.
func captureExplainStdout(t *testing.T, pipeline string, rest []string) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan []byte, 1)
	go func() {
		buf, _ := io.ReadAll(r)
		done <- buf
	}()
	defer func() { os.Stdout = orig }()
	if err := printPipelinePlan(pipeline, rest); err != nil {
		_ = w.Close()
		<-done
		t.Fatalf("printPipelinePlan(%v): %v", rest, err)
	}
	_ = w.Close()
	return <-done
}

// nodeIDsFromSnapshot decodes a planSnapshot JSON blob and returns
// its sorted top-level node IDs. Sorting normalizes any deterministic-
// but-still-structural ordering differences so the two paths can be
// compared as sets.
func nodeIDsFromSnapshot(t *testing.T, raw []byte) []string {
	t.Helper()
	var snap planSnapshot
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v\nraw=%s", err, string(raw))
	}
	ids := make([]string, 0, len(snap.Nodes))
	for _, n := range snap.Nodes {
		ids = append(ids, n.ID)
	}
	sort.Strings(ids)
	return ids
}

// TestPrintPipelinePlan_SkipParityAcrossOutputFlags is the load-bearing
// regression: invoking the explain entrypoint with --skip alone vs.
// --skip alongside -o json must produce the exact same node set. The
// fix lives in printPipelinePlan / stripExplainOutputFlags
// (orchestrator/main.go).
func TestPrintPipelinePlan_SkipParityAcrossOutputFlags(t *testing.T) {
	cases := []struct {
		name string
		rest []string
	}{
		{"skip only", []string{"--skip", "artifact"}},
		{"skip with -o json (space)", []string{"--skip", "artifact", "-o", "json"}},
		{"skip with --output=json", []string{"--skip", "artifact", "--output=json"}},
		{"skip with --json", []string{"--skip", "artifact", "--json"}},
		{"skip with --json=true", []string{"--skip", "artifact", "--json=true"}},
		{"skip with -o=json", []string{"--skip", "artifact", "-o=json"}},
	}
	var baseline []string
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureExplainStdout(t, "explain-skip-test", tc.rest)
			ids := nodeIDsFromSnapshot(t, out)
			// "artifact" must NOT appear -- the SkipFilter dropped it.
			for _, id := range ids {
				if id == "artifact" {
					t.Fatalf("--skip artifact ignored: nodes=%v\nraw=%s", ids, string(out))
				}
			}
			if i == 0 {
				baseline = ids
				return
			}
			if !equalStringSlices(ids, baseline) {
				t.Fatalf("node set diverges from baseline\nbaseline=%v\ngot     =%v\nraw=%s",
					baseline, ids, string(out))
			}
		})
	}
}

// TestStripExplainOutputFlags_RemovesWrapperFlagsKeepsRest pins the
// helper's contract: every shape of -o / --output / --json (with or
// without =, with or without a separate value) is consumed; every
// other token survives in original order.
func TestStripExplainOutputFlags_RemovesWrapperFlagsKeepsRest(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{[]string{"--skip", "artifact"}, []string{"--skip", "artifact"}},
		{[]string{"--skip", "artifact", "-o", "json"}, []string{"--skip", "artifact"}},
		{[]string{"-o", "json", "--skip", "artifact"}, []string{"--skip", "artifact"}},
		{[]string{"--skip", "artifact", "--output=json"}, []string{"--skip", "artifact"}},
		{[]string{"--skip", "artifact", "--json"}, []string{"--skip", "artifact"}},
		{[]string{"--skip", "artifact", "--json=true"}, []string{"--skip", "artifact"}},
		{[]string{"--skip", "artifact", "-o=json"}, []string{"--skip", "artifact"}},
		{[]string{"--only", "build", "-o", "table"}, []string{"--only", "build"}},
		// Defensive: -o followed by a flag (malformed) must not eat
		// the next flag.
		{[]string{"-o", "--skip", "artifact"}, []string{"--skip", "artifact"}},
	}
	for _, tc := range cases {
		got := stripExplainOutputFlags(tc.in)
		if !equalStringSlices(got, tc.want) {
			t.Errorf("stripExplainOutputFlags(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
