package orchestrator

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

func TestPrintPipelinePlan_SkipParityAcrossOutputFlags(t *testing.T) {
	cases := []struct {
		name string
		rest []string
	}{
		{"skip only", []string{"--skip", "artifact"}},
		{"skip with -o json (space)", []string{"--skip", "artifact", "-o", "json"}},
		{"skip with --output=json", []string{"--skip", "artifact", "--output=json"}},
		{"skip with -o=json", []string{"--skip", "artifact", "-o=json"}},
	}
	var baseline []string
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureExplainStdout(t, "explain-skip-test", tc.rest)
			ids := nodeIDsFromSnapshot(t, out)
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

func TestStripExplainOutputFlags_RemovesWrapperFlagsKeepsRest(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{[]string{"--skip", "artifact"}, []string{"--skip", "artifact"}},
		{[]string{"--skip", "artifact", "-o", "json"}, []string{"--skip", "artifact"}},
		{[]string{"-o", "json", "--skip", "artifact"}, []string{"--skip", "artifact"}},
		{[]string{"--skip", "artifact", "--output=json"}, []string{"--skip", "artifact"}},
		{[]string{"--skip", "artifact", "-o=json"}, []string{"--skip", "artifact"}},
		{[]string{"--only", "build", "-o", "table"}, []string{"--only", "build"}},
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
