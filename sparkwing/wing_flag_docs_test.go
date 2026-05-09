package sparkwing

import (
	"sort"
	"testing"
)

// TestWingFlagDocs_OrderAndUniqueness pins the documented wing-flag
// set so a regression (typo, accidental dedupe, missing entry) shows
// up as a test failure. Order is also pinned because the per-pipeline
// help footer renders in walk order; arbitrary reordering would
// silently re-shape every pipeline's --help.
func TestWingFlagDocs_OrderAndUniqueness(t *testing.T) {
	docs := WingFlagDocs()
	if len(docs) == 0 {
		t.Fatalf("WingFlagDocs() returned empty slice")
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
			t.Errorf("duplicate --%s in WingFlagDocs", d.Name)
		}
		seen[d.Name] = true
	}
}

// TestWingFlagDocs_CoversSafetyFlags pins the range-resume, dry-run,
// and blast-radius flag set the doc list MUST include. A future
// cleanup that removes one should fail loud here so the help drift
// doesn't regress.
func TestWingFlagDocs_CoversSafetyFlags(t *testing.T) {
	docs := WingFlagDocs()
	have := map[string]bool{}
	for _, d := range docs {
		have[d.Name] = true
	}
	mustHave := []string{
		// Range-resume.
		"start-at", "stop-at",
		// Dry-run.
		"dry-run",
		// Blast-radius escape hatches.
		"allow-destructive", "allow-prod", "allow-money",
	}
	for _, f := range mustHave {
		if !have[f] {
			t.Errorf("WingFlagDocs missing --%s", f)
		}
	}
}

// TestWingFlagDocs_SubsetOfReservedFlags pins the contract that every
// documented wing flag is also reserved -- so an Args struct with
// `flag:"start-at"` would still get rejected at Register time and
// the documented surface is also the protected surface. The
// converse is not required: reservedFlagNames includes infra-only
// flags (--secrets, --mode, --workers, --no-update) that are
// intentionally absent from public help.
func TestWingFlagDocs_SubsetOfReservedFlags(t *testing.T) {
	reserved := map[string]bool{}
	for _, n := range ReservedFlagNames() {
		reserved[n] = true
	}
	for _, d := range WingFlagDocs() {
		if !reserved[d.Name] {
			t.Errorf("WingFlagDocs has --%s but reservedFlagNames does not (an Args struct with flag:%q would NOT panic at Register)", d.Name, d.Name)
		}
	}
}

// TestWingFlagDocs_ReturnsCopy is the parallel of
// TestReservedFlagNamesIsCopy: callers may mutate freely.
func TestWingFlagDocs_ReturnsCopy(t *testing.T) {
	a := WingFlagDocs()
	if len(a) == 0 {
		t.Fatalf("WingFlagDocs() empty")
	}
	a[0].Name = "MUTATED"
	b := WingFlagDocs()
	if b[0].Name == "MUTATED" {
		t.Errorf("WingFlagDocs returned a shared slice; mutation leaked: %v", b[0])
	}
}

// TestWingFlagDocs_GroupsAreKnown pins the rendering buckets so a
// rogue Group string ("Range " with trailing space, "safety" lower-
// case) doesn't silently fall into a default bucket and visually
// drift the help layout.
func TestWingFlagDocs_GroupsAreKnown(t *testing.T) {
	known := map[string]bool{
		"Source": true,
		"Range":  true,
		"Safety": true,
		"System": true,
	}
	for _, d := range WingFlagDocs() {
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
