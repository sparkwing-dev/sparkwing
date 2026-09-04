package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/gitenv"
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

func TestNosecRE_RequiresRuleAndReason(t *testing.T) {
	allow := []string{
		"// #nosec G703 -- the path comes from this user's own environment",
		"//#nosec G404 -- retry jitter, not a security decision",
		"// #nosec G122,G703 -- the walk stays inside a directory this process owns",
	}
	for _, s := range allow {
		if !nosecRE.MatchString(s) {
			t.Errorf("expected %q to match the nosec allowlist", s)
		}
	}
	deny := []string{
		"// #nosec",
		"// #nosec G703",
		"// #nosec G703 --",
		"// #nosec -- no rule named",
		"// #nosec g703 -- lowercase rule",
		"// nosec G703 -- no hash",
	}
	for _, s := range deny {
		if nosecRE.MatchString(s) {
			t.Errorf("expected %q NOT to match the nosec allowlist", s)
		}
	}
}

func TestCheckFile_RejectsProseRidingOnANosec(t *testing.T) {
	src := `package widget

import "os"

func smuggled() {
	// #nosec G999 -- placeholder
	// This paragraph is arbitrary prose that the comment gate still rejects.
	// A second line of free narrative, riding behind a fabricated rule id.
	_, _ = os.Stat(os.Getenv("WIDGET_PATH"))
}

func tagged() {
	// safety: a tagged group is no licence for an annotation to carry narrative.
	// #nosec G703 -- the path comes from this user's own environment
	_, _ = os.Stat(os.Getenv("WIDGET_PATH"))
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
	if len(got) != 2 || got[0].line != 6 || got[1].line != 13 {
		t.Fatalf("violations = %+v, want the multi-line nosec groups at lines 6 and 13", got)
	}
	for _, v := range got {
		if !strings.Contains(v.text, "stands alone on one line") {
			t.Errorf("violation %q does not name the one-line rule", v.text)
		}
	}
}

func TestCheckFile_AllowsAnnotatedNosec(t *testing.T) {
	src := `package widget

import "os"

func annotated() {
	// #nosec G703 -- the path comes from this user's own environment
	_, _ = os.Stat(os.Getenv("WIDGET_PATH"))
}

func naked() {
	// #nosec
	_, _ = os.Stat(os.Getenv("WIDGET_PATH"))
}

func unjustified() {
	// #nosec G703
	_, _ = os.Stat(os.Getenv("WIDGET_PATH"))
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
	if len(got) != 2 || got[0].line != 11 || got[1].line != 16 {
		t.Fatalf("violations = %+v, want the naked and unjustified annotations", got)
	}
}

func TestScopedAdds_FailsWhenTheBaseCannotBeResolved(t *testing.T) {
	if _, err := scopedAdds(t.TempDir(), false, "origin/main"); err == nil {
		t.Fatal("scopedAdds reported a diff outside a repository, so the gate would pass ungated")
	}
}

func TestDiffFailure_NamesTheFixAndTheEscape(t *testing.T) {
	msg := diffFailure("origin/main", errors.New("fatal: bad revision"))
	for _, want := range []string{"origin/main", "nothing was gated", "git fetch origin main", "-allow-no-diff"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the diff failure does not name %q: %s", want, msg)
		}
	}
	if got := diffFailure("", errors.New("boom")); !strings.Contains(got, "the staged diff") {
		t.Errorf("the staged-mode failure does not name its scope: %s", got)
	}
}

func TestUsage_NamesThePositionalAndTheCaps(t *testing.T) {
	for _, want := range []string{
		"<root>",
		"one directory to walk",
		"takes no list of files",
		"at most 4 lines",
		"120 characters",
	} {
		if !strings.Contains(usageText, want) {
			t.Errorf("the usage text does not name %q:\n%s", want, usageText)
		}
	}
}

func TestAdvice_TellsTheAgentToTagRatherThanDelete(t *testing.T) {
	for _, want := range []string{
		"tag the comment, do not delete it",
		"hack:/safety:/bug:/perf:",
		"say why in one short line",
		"belongs under hack: or safety:",
	} {
		if !strings.Contains(advice, want) {
			t.Errorf("the failure advice does not say %q:\n%s", want, advice)
		}
	}
	if strings.Contains(advice, "Fix: delete the comment") {
		t.Errorf("the failure advice still leads with deleting the comment:\n%s", advice)
	}
}

func TestCheckFile_NosecViolationsNameTheirRule(t *testing.T) {
	src := `package widget

import "os"

func adjacent() {
	// safety: the path is this process's own
	// #nosec G703 -- the path comes from this user's own environment
	_, _ = os.Stat(os.Getenv("WIDGET_PATH"))
}

func malformed() {
	// #nosec G703
	_, _ = os.Stat(os.Getenv("WIDGET_PATH"))
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
	if len(got) != 2 {
		t.Fatalf("violations = %+v, want the adjacent and malformed annotations", got)
	}
	for _, want := range []string{"nosec adjacency", "rides past this gate unread", "put a blank line"} {
		if !strings.Contains(got[0].text, want) {
			t.Errorf("the adjacency violation %q does not say %q", got[0].text, want)
		}
	}
	if !strings.Contains(got[1].text, "nosec form") {
		t.Errorf("the malformed violation %q does not name the form rule", got[1].text)
	}
}

func TestScopedAdds_BaseChargesUntrackedFiles(t *testing.T) {
	repo := newGatedRepo(t)
	writeSource(t, filepath.Join(repo, "untracked_test.go"), "package p\n\nfunc h() {\n\t// narrate the new test\n}\n")
	writeSource(t, filepath.Join(repo, "untracked.txt"), "not go\n")

	added, err := scopedAdds(repo, false, "main")
	if err != nil {
		t.Fatalf("scopedAdds: %v", err)
	}
	if !added["untracked_test.go"][4] {
		t.Errorf("the base diff reported %v; a comment in an untracked file is not charged to the branch", added)
	}
	if _, ok := added["untracked.txt"]; ok {
		t.Errorf("the base diff reported %v; a non-Go untracked file is in scope", added)
	}
}

func TestScopedAdds_StagedIgnoresUntrackedFiles(t *testing.T) {
	repo := newGatedRepo(t)
	writeSource(t, filepath.Join(repo, "untracked.go"), "package p\n\nfunc h() {\n\t// narrate\n}\n")

	t.Setenv("GIT_INDEX_FILE", "")
	t.Setenv(gitenv.GateIndexVar, "")

	added, err := scopedAdds(repo, true, "")
	if err != nil {
		t.Fatalf("scopedAdds: %v", err)
	}
	if len(added) != 0 {
		t.Errorf("the staged diff reported %v; an untracked file the commit does not carry is charged to it", added)
	}
}
