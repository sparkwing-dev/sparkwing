package sparkwing

import (
	"sort"
	"testing"
)

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

func TestSparkwingFlagDocs_AllSwPrefixed(t *testing.T) {
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
