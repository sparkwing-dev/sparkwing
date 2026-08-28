package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFile_AllowsDocAndTagsRejectsNarration(t *testing.T) {
	src := `// Package widget does widget things.
package widget

// Adder sums two ints.
type Adder struct {
	// A is the first addend.
	A int
	B int // the second addend
}

// Sum adds the fields and explains nothing extra.
func (a Adder) Sum() int {
	// this narration restates the code and must be rejected
	total := a.A + a.B
	// hack: round-trip through float to match the legacy wire format
	_ = float64(total)
	// safety: callers hold the lock here
	return total //nolint:something
}

// helpers ----------------------------------------------------

func unused() {} // bug: never called, kept for symmetry
`
	dir := t.TempDir()
	path := filepath.Join(dir, "widget.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}

	gotLines := map[int]bool{}
	for _, v := range got {
		gotLines[v.line] = true
	}

	wantRejected := []int{13, 21}
	for _, ln := range wantRejected {
		if !gotLines[ln] {
			t.Errorf("expected line %d to be rejected, but it was allowed", ln)
		}
	}

	allowed := []int{1, 4, 6, 8, 11, 15, 17, 18, 23}
	for _, ln := range allowed {
		if gotLines[ln] {
			t.Errorf("expected line %d to be allowed, but it was rejected", ln)
		}
	}
}

func TestCheckFile_RejectsDocsOnUnexportedDeclarations(t *testing.T) {
	src := `package widget

// exportedValue is internal.
const exportedValue = 1

// helper is internal.
func helper() {}

// hidden is internal.
type hidden struct {
	// value is internal.
	value int
}

// Public is exported.
type Public struct {
	// Value is exported.
	Value int
}

// Build is exported.
func Build() {}
`
	path := filepath.Join(t.TempDir(), "widget.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}

	gotLines := map[int]bool{}
	for _, v := range got {
		gotLines[v.line] = true
	}
	for _, line := range []int{3, 6, 9, 11} {
		if !gotLines[line] {
			t.Errorf("expected internal documentation on line %d to be rejected", line)
		}
	}
	for _, line := range []int{15, 17, 21} {
		if gotLines[line] {
			t.Errorf("expected exported API documentation on line %d to be allowed", line)
		}
	}
}

func TestCheckFile_AllowsDocsOnExportedEmbeddedGenerics(t *testing.T) {
	src := `package widget

type Box[T any] struct{}

type Public struct {
	// Box carries the value.
	Box[int]
	// Other carries another value.
	*Other[string, int]
}

type Other[K, V any] struct{}
`
	path := filepath.Join(t.TempDir(), "widget.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("violations = %+v, want exported embedded generic docs allowed", got)
	}
}

func TestCheckFile_RejectsLongTaggedComments(t *testing.T) {
	src := `package widget

func allowed() {
	// safety: one
	// two
	// three
	// four
}

func rejected() {
	// safety: one
	// two
	// three
	// four
	// five
}

func tooWide() {
	// safety: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
}

func blockBypass() {
	// safety: tagged line
	/* two
	three
	four
	five
	six */
}

func emptyRationale() {
	// safety:
}
`
	path := filepath.Join(t.TempDir(), "widget.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	if len(got) != 4 || got[0].line != 11 || got[1].line != 19 || got[2].line != 23 || got[3].line != 32 {
		t.Fatalf("violations = %+v, want the five-line, overlong, block-bypass, and empty tagged comments", got)
	}
}

func TestCheckFile_AllowsExampleOutputMarkers(t *testing.T) {
	src := `package widget_test

import "fmt"

// ExampleAdder shows the call shape.
func ExampleAdder() {
	fmt.Println("3")
	// Output: 3
}

// ExampleAdder_unordered shows unordered output.
func ExampleAdder_unordered() {
	fmt.Println("a")
	fmt.Println("b")
	// Unordered output:
	// a
	// b
}

// ExampleAdder_narration mixes a disallowed body comment with an Output marker.
func ExampleAdder_narration() {
	// this narration restates the code and must be rejected
	fmt.Println("9")
	// Output: 9
}

// notAnExample is a normal function; its Output marker is narration, not a
// testable-example directive, so it must still be rejected.
func notAnExample() {
	// Output: this is not a testable example
	fmt.Println("x")
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "example_widget_test.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}

	gotLines := map[int]bool{}
	for _, v := range got {
		gotLines[v.line] = true
	}

	wantRejected := []int{22, 30}
	for _, ln := range wantRejected {
		if !gotLines[ln] {
			t.Errorf("expected line %d to be rejected, but it was allowed", ln)
		}
	}

	allowed := []int{8, 15, 24}
	for _, ln := range allowed {
		if gotLines[ln] {
			t.Errorf("expected Output marker on line %d to be allowed, but it was rejected", ln)
		}
	}
}

func TestCheckFile_RejectsOpaqueTicketLabelsInDocumentation(t *testing.T) {
	src := `// Package widget implements BW-123 behavior.
package widget

// Add preserves the bw-456 compatibility rule.
func Add() {}

// Remove accepts BWT-789 because it is not a ticket label.
func Remove() {}
`
	path := filepath.Join(t.TempDir(), "widget.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("violations = %+v, want both opaque ticket labels rejected", got)
	}
}

func TestIsDirective(t *testing.T) {
	cases := map[string]bool{
		"//go:build linux":                true,
		"//go:embed docs":                 true,
		"//nolint:errcheck":               true,
		"//lint:ignore U1000 reason":      true,
		"//lint:file-ignore U1000 reason": true,
		"//why:not allowed":               false,
		"// hack: not a dir":              false,
		"// regular comment":              false,
		"//just text":                     false,
		"//TODO:nope":                     false,
	}
	for text, want := range cases {
		if got := isDirective(text); got != want {
			t.Errorf("isDirective(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestCheckFile_DirectivesDoNotCloakNarration(t *testing.T) {
	src := `package widget

//go:generate echo generated
// this ordinary narration is not a directive
var generatedValue int

//nolint:unused
// this is ordinary narration too
var lintedValue int

//go:generate echo one
//go:generate echo two
var generatedTwice int
`
	path := filepath.Join(t.TempDir(), "widget.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := checkFile(path)
	if err != nil {
		t.Fatalf("checkFile: %v", err)
	}
	if len(got) != 2 || got[0].line != 4 || got[1].line != 8 {
		t.Fatalf("violations = %+v, want only narration adjacent to directives", got)
	}
}

func TestTagRE_OnlyTheFourTags(t *testing.T) {
	allow := []string{"// hack: x", "//hack: x", "// HACK: x", "// safety: x", "// bug: x", "// perf: x"}
	for _, s := range allow {
		if !tagRE.MatchString(s) {
			t.Errorf("expected %q to match the tag allowlist", s)
		}
	}
	deny := []string{"// note: x", "// why: x", "// todo: x", "// hacky: x", "// the bug is gone"}
	for _, s := range deny {
		if tagRE.MatchString(s) {
			t.Errorf("expected %q NOT to match the tag allowlist", s)
		}
	}
}
