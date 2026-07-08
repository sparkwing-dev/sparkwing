package sparkwing

import (
	"context"
	"testing"
)

func noop(ctx context.Context) error { return nil }

func TestOutputs_UnionDedupesAndSkipsEmpty(t *testing.T) {
	p := NewPlan()
	n := Job(p, "build", noop).
		Outputs("dist/**", "").
		Outputs("dist/**", "logs/*.txt")

	got := n.OutputGlobs()
	want := []string{"dist/**", "logs/*.txt"}
	if len(got) != len(want) {
		t.Fatalf("OutputGlobs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("OutputGlobs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	got[0] = "mutated"
	if n.OutputGlobs()[0] == "mutated" {
		t.Fatal("OutputGlobs must return a copy, not the backing slice")
	}
}

func TestConsumes_ImpliesNeedsAndRecordsEdge(t *testing.T) {
	p := NewPlan()
	prod := Job(p, "build", noop).Outputs("dist/**")
	con := Job(p, "deploy", noop).Consumes(prod, Into("artifacts/build"))

	deps := con.DepIDs()
	if len(deps) != 1 || deps[0] != "build" {
		t.Fatalf("Consumes should imply Needs(build); DepIDs = %v", deps)
	}
	edges := con.ConsumeEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 consume edge, got %d", len(edges))
	}
	if edges[0].Producer != "build" || edges[0].Into != "artifacts/build" {
		t.Fatalf("edge = %+v, want {build artifacts/build}", edges[0])
	}
}

func TestConsumes_RepeatOverwritesSameProducer(t *testing.T) {
	p := NewPlan()
	prod := Job(p, "build", noop).Outputs("dist/**")
	con := Job(p, "deploy", noop).
		Consumes(prod, Into("first")).
		Consumes(prod, Into("second"))

	edges := con.ConsumeEdges()
	if len(edges) != 1 {
		t.Fatalf("repeating Consumes for one producer should keep a single edge, got %d", len(edges))
	}
	if edges[0].Into != "second" {
		t.Fatalf("last Consumes should win; Into = %q, want %q", edges[0].Into, "second")
	}
	if got := con.DepIDs(); len(got) != 1 {
		t.Fatalf("repeated Consumes should not duplicate the Needs edge; DepIDs = %v", got)
	}
}

func TestValidateArtifactEdges_ConsumeWithoutOutputsIsError(t *testing.T) {
	p := NewPlan()
	prod := Job(p, "build", noop)
	Job(p, "deploy", noop).Consumes(prod)

	err := p.validateArtifactEdges()
	if err == nil {
		t.Fatal("consuming a producer with no Outputs must be a plan-time error")
	}
	if !contains(err.Error(), "deploy") || !contains(err.Error(), "build") {
		t.Fatalf("error should name both nodes; got %v", err)
	}
}

func TestValidateArtifactEdges_OutputsDoesNotRequireCache(t *testing.T) {
	p := NewPlan()
	prod := Job(p, "build", noop).Outputs("dist/**")
	Job(p, "deploy", noop).Consumes(prod)

	if err := p.validateArtifactEdges(); err != nil {
		t.Fatalf("Outputs without Cache should be valid; got %v", err)
	}
	if len(p.LintWarnings()) != 0 {
		t.Fatalf("single producer should warn nothing; got %v", p.LintWarnings())
	}
}

func TestValidateArtifactEdges_OverlapWarnsOnSharedDir(t *testing.T) {
	p := NewPlan()
	a := Job(p, "a", noop).Outputs("dist/**")
	b := Job(p, "b", noop).Outputs("dist/extra.txt")
	Job(p, "merge", noop).Consumes(a).Consumes(b)

	if err := p.validateArtifactEdges(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	warns := p.LintWarnings()
	if len(warns) != 1 {
		t.Fatalf("expected 1 overlap warning, got %d (%v)", len(warns), warns)
	}
	if warns[0].Code != "artifact-stage-overlap" || warns[0].NodeID != "merge" {
		t.Fatalf("warning = %+v, want code artifact-stage-overlap on merge", warns[0])
	}
}

func TestValidateArtifactEdges_DistinctShardsDoNotWarn(t *testing.T) {
	p := NewPlan()
	a := Job(p, "a", noop).Outputs("coverage/shard-1.json")
	b := Job(p, "b", noop).Outputs("coverage/shard-2.json")
	Job(p, "agg", noop).Consumes(a).Consumes(b)

	if err := p.validateArtifactEdges(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := p.LintWarnings(); len(w) != 0 {
		t.Fatalf("distinct shard paths must not warn; got %v", w)
	}
}

func TestValidateArtifactEdges_IntoSeparatesOverlap(t *testing.T) {
	p := NewPlan()
	a := Job(p, "a", noop).Outputs("dist/**")
	b := Job(p, "b", noop).Outputs("dist/**")
	Job(p, "merge", noop).
		Consumes(a, Into("a")).
		Consumes(b, Into("b"))

	if err := p.validateArtifactEdges(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := p.LintWarnings(); len(w) != 0 {
		t.Fatalf("distinct Into prefixes must not warn; got %v", w)
	}
}

func TestValidateArtifactEdges_IntoCollisionWarns(t *testing.T) {
	p := NewPlan()
	a := Job(p, "a", noop).Outputs("dist/**")
	b := Job(p, "b", noop).Outputs("dist/**")
	Job(p, "merge", noop).
		Consumes(a, Into("shared")).
		Consumes(b, Into("shared"))

	if err := p.validateArtifactEdges(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w := p.LintWarnings(); len(w) != 1 {
		t.Fatalf("shared Into prefix must warn once; got %v", w)
	}
}

func TestGlobLiteralPrefix(t *testing.T) {
	cases := map[string]string{
		"dist/**":                  "dist",
		"coverage/shard-1.json":    "coverage/shard-1.json",
		"*.json":                   "",
		"a/b/c.txt":                "a/b/c.txt",
		"a/b/*/d":                  "a/b",
		"pkg/**/testdata/*.golden": "pkg",
	}
	for in, want := range cases {
		if got := globLiteralPrefix(in); got != want {
			t.Errorf("globLiteralPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPathContains(t *testing.T) {
	overlap := [][2]string{
		{"dist", "dist"},
		{"dist", "dist/sub"},
		{"", "anything"},
		{"a/b", "a/b/c/d"},
	}
	for _, c := range overlap {
		if !pathContains(c[0], c[1]) {
			t.Errorf("pathContains(%q,%q) = false, want true", c[0], c[1])
		}
	}
	disjoint := [][2]string{
		{"dist", "distinct"},
		{"coverage/shard-1.json", "coverage/shard-2.json"},
		{"a/b", "a/c"},
	}
	for _, c := range disjoint {
		if pathContains(c[0], c[1]) {
			t.Errorf("pathContains(%q,%q) = true, want false", c[0], c[1])
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
