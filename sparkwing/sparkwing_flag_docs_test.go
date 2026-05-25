package sparkwing

import (
	"sort"
	"testing"
)

// TestSparkwingFlagDocs_OrderAndUniqueness pins the documented
// sparkwing-owned flag set so a regression (typo, accidental dedupe,
// missing entry) shows up as a test failure. Order is also pinned
// because the per-pipeline help footer renders in walk order;
// arbitrary reordering would silently re-shape every pipeline's
// --help.
func TestSparkwingFlagDocs_OrderAndUniqueness(t *testing.T) {
	docs := SparkwingFlagDocs()
	if len(docs) == 0 {
		t.Fatalf("SparkwingFlagDocs() returned empty slice")
	}
	seen := map[string]bool{}
	for _, d := range docs {
		if d.Name == "" {
			t.Errorf("empty Name in entry %+v", d)
		}
		if d.Desc == "" {
			t.Errorf("empty Desc on --%s", d.Name)
		}
		if d.Group == "" {
			t.Errorf("empty Group on --%s", d.Name)
		}
		if seen[d.Name] {
			t.Errorf("duplicate --%s in SparkwingFlagDocs", d.Name)
		}
		seen[d.Name] = true
	}
}

// TestSparkwingFlagDocs_CoversSafetyFlags pins the range-resume,
// dry-run, and risk-label flag set the doc list MUST include.
func TestSparkwingFlagDocs_CoversSafetyFlags(t *testing.T) {
	docs := SparkwingFlagDocs()
	have := map[string]bool{}
	for _, d := range docs {
		have[d.Name] = true
	}
	mustHave := []string{
		"sw-start-at", "sw-stop-at",
		"sw-dry-run",
		"sw-allow",
	}
	for _, f := range mustHave {
		if !have[f] {
			t.Errorf("SparkwingFlagDocs missing --%s", f)
		}
	}
}

// TestSparkwingFlagDocs_AllSwPrefixed pins that every documented
// sparkwing-owned flag carries the sw- prefix. The prefix is the
// entire reservation mechanism -- it lets pipeline-author Inputs
// flags occupy the unprefixed namespace without collision.
func TestSparkwingFlagDocs_AllSwPrefixed(t *testing.T) {
	// flatTopLevel lists the deliberately unprefixed sparkwing-owned
	// flags. The v0.5.0 config redesign reserves --profile ("run / read
	// against this storage profile") and --target ("which pipeline
	// deployment environment") as flat top-level flags, claiming them
	// from the pipeline-author namespace by design.
	flatTopLevel := map[string]bool{"profile": true, "target": true}
	for _, d := range SparkwingFlagDocs() {
		if flatTopLevel[d.Name] {
			continue
		}
		if len(d.Name) < 3 || d.Name[:3] != "sw-" {
			t.Errorf("--%s lacks sw- prefix; every sparkwing-owned flag must be sw-prefixed so pipeline-author flags are collision-free", d.Name)
		}
	}
}

// TestSparkwingFlagDocs_ReturnsCopy ensures callers may mutate the
// returned slice freely without affecting subsequent calls.
func TestSparkwingFlagDocs_ReturnsCopy(t *testing.T) {
	a := SparkwingFlagDocs()
	if len(a) == 0 {
		t.Fatalf("SparkwingFlagDocs() empty")
	}
	a[0].Name = "MUTATED"
	b := SparkwingFlagDocs()
	if b[0].Name == "MUTATED" {
		t.Errorf("SparkwingFlagDocs returned a shared slice; mutation leaked: %v", b[0])
	}
}

// TestSparkwingFlagDocs_GroupsAreKnown pins the rendering buckets so
// a rogue Group string ("system " with trailing space, "System "
// with capitalization drift) doesn't silently fall into a default
// bucket. Every sparkwing-owned flag belongs to the single "System"
// bucket; pipeline-author flags get their own "Pipeline Args" bucket
// in the render layer.
func TestSparkwingFlagDocs_GroupsAreKnown(t *testing.T) {
	known := map[string]bool{
		"System": true,
	}
	for _, d := range SparkwingFlagDocs() {
		if !known[d.Group] {
			t.Errorf("--%s has unknown Group %q (expected one of: %v)", d.Name, d.Group, sortedBoolKeys(known))
		}
	}
}

func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
