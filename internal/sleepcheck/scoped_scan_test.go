package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestScopedScanMatchesFullScanFiltering(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "changed_test.go"), "package fixture\nimport \"time\"\nfunc TestChanged() { time.Sleep(time.Second) }\n")
	writeFile(t, filepath.Join(root, "old_test.go"), "package fixture\nimport \"time\"\nfunc TestOld() { time.Sleep(time.Second) }\n")
	writeFile(t, filepath.Join(root, "broken_test.go"), "package fixture\nfunc TestBroken(\n")
	full, unread, err := scan(root)
	if err != nil || len(full) != 2 || len(unread) != 1 {
		t.Fatalf("full fixture = %v, %v, %v", full, unread, err)
	}
	for name, added := range map[string]map[string]map[int]bool{
		"empty":          {},
		"changed":        {"changed_test.go": {3: true}},
		"different line": {"changed_test.go": {1: true}},
		"parse failure":  {"broken_test.go": {2: true}},
	} {
		t.Run(name, func(t *testing.T) {
			got, bad, err := scanScoped(root, added)
			if err != nil {
				t.Fatal(err)
			}
			if want := onlyAdded(full, added); !reflect.DeepEqual(onlyAdded(got, added), want) {
				t.Fatalf("scoped findings = %v; full filtered = %v", got, want)
			}
			if want := onlyChanged(unread, root, added); unreadableFailure(bad) != unreadableFailure(want) {
				t.Fatalf("scoped parse failures = %v; full filtered = %v", bad, want)
			}
		})
	}
}

func TestScopedScanKeepsGlobalBoundaryAudits(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "changed_test.go"), "package fixture\nfunc TestChanged() {}\n")
	writeFile(t, filepath.Join(root, "outside_test.go"), "package fixture\n// sleepcheck:external-boundary unapproved marker\nfunc TestOutside() {}\n")
	findings, unread, err := scanScoped(root, map[string]map[int]bool{"changed_test.go": {2: true}})
	if err != nil || len(findings) != 0 || len(unread) != 0 {
		t.Fatalf("scoped scan = %v, %v, %v", findings, unread, err)
	}
	allowed, stale := approvedWaits(root, []externalBoundary{{file: "outside_test.go", function: "TestOutside", duration: "time.Second", marker: "// sleepcheck:external-boundary unapproved marker"}})
	markers, err := unconsumedBoundaryMarkers(root, allowed)
	if err != nil || len(stale) != 1 || len(markers) != 1 || markers[0].file != "outside_test.go" {
		t.Fatalf("outside-scope audits = stale %v, markers %v, error %v", stale, markers, err)
	}
	writeFile(t, filepath.Join(root, "literal_test.go"), "package fixture\nvar marker = \"// sleepcheck:external-boundary not a comment\"\n")
	markers, err = unconsumedBoundaryMarkers(root, allowed)
	if err != nil || len(markers) != 1 {
		t.Fatalf("marker text in a string became a comment: %v, %v", markers, err)
	}
}
