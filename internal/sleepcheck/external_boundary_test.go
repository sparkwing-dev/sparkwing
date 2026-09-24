package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovedWaitsRequireTheExactBoundary(t *testing.T) {
	const path = "case_test.go"
	rule := externalBoundary{
		file: path, function: "TestBoundary", duration: "2 * time.Second",
		marker: "// sleepcheck:external-boundary external process needs a cleanup bound",
	}
	source := `package p
import "time"
func TestBoundary() {
	select {
	// sleepcheck:external-boundary external process needs a cleanup bound
	case <-time.After(2 * time.Second):
	}
}
`
	for _, tc := range []struct {
		name    string
		source  string
		allowed int
		stale   int
		left    int
	}{
		{name: "exact", source: source, allowed: 1},
		{name: "missing marker", source: strings.Replace(source, rule.marker, "", 1), stale: 1, left: 1},
		{name: "changed marker", source: strings.Replace(source, rule.marker, rule.marker+" changed", 1), stale: 1, left: 1},
		{name: "changed duration", source: strings.Replace(source, "2 * time.Second", "3 * time.Second", 1), stale: 1, left: 1},
		{name: "moved function", source: strings.Replace(source, "TestBoundary", "TestOther", 1), stale: 1, left: 1},
		{name: "moved to standalone receive", source: strings.Replace(source,
			"\tselect {\n\t// sleepcheck:external-boundary external process needs a cleanup bound\n\tcase <-time.After(2 * time.Second):\n\t}",
			"\t// sleepcheck:external-boundary external process needs a cleanup bound\n\t<-time.After(2 * time.Second)", 1), stale: 1, left: 1},
		{name: "extra wait", source: strings.Replace(source, "\t}\n}", "\tcase <-time.After(time.Second):\n\t}\n}", 1), allowed: 1, left: 1},
		{name: "extra sleep", source: strings.Replace(source, "\t}\n}", "\t}\n\ttime.Sleep(time.Second)\n}", 1), allowed: 1, left: 1},
		{name: "extra wait on approved line", source: strings.Replace(source, "case <-time.After(2 * time.Second):", "case <-time.After(2 * time.Second): <-time.After(time.Second)", 1), allowed: 1, left: 1},
		{name: "extra sleep on approved line", source: strings.Replace(source, "case <-time.After(2 * time.Second):", "case <-time.After(2 * time.Second): time.Sleep(time.Second)", 1), allowed: 1, left: 1},
		{name: "stale entry", source: strings.Replace(source, "\tcase <-time.After(2 * time.Second):", "\tcase <-make(chan struct{}):", 1), stale: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, path), []byte(tc.source), 0o644); err != nil {
				t.Fatal(err)
			}
			allowed, stale := approvedWaits(root, []externalBoundary{rule})
			findings, _, err := scan(root)
			if err != nil {
				t.Fatal(err)
			}
			left := withoutApproved(findings, allowed)
			if len(allowed[path]) != tc.allowed || len(stale) != tc.stale || len(left) != tc.left {
				t.Fatalf("approved = %v, stale = %v, findings = %v; want %d, %d, %d",
					allowed, stale, left, tc.allowed, tc.stale, tc.left)
			}
		})
	}
}

func TestUnconsumedExternalBoundaryMarkerFails(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "case_test.go")
	source := `package p
import "time"
func TestBoundary() {
	select {
	// sleepcheck:external-boundary external process needs a cleanup bound
	case <-time.After(2 * time.Second):
	}
}
// sleepcheck:external-boundary unused exception
`
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	rule := externalBoundary{
		file: "case_test.go", function: "TestBoundary", duration: "2 * time.Second",
		marker: "// sleepcheck:external-boundary external process needs a cleanup bound",
	}
	allowed, stale := approvedWaits(root, []externalBoundary{rule})
	if len(stale) != 0 || len(allowed[rule.file]) != 1 {
		t.Fatalf("valid wait rejected: approved=%v stale=%v", allowed, stale)
	}
	unused, err := unconsumedBoundaryMarkers(root, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if len(unused) != 1 || unused[0].line != 9 {
		t.Fatalf("unconsumed markers = %v, want only line 9", unused)
	}
}
