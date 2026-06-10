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

func TestIsDirective(t *testing.T) {
	cases := map[string]bool{
		"//go:build linux":   true,
		"//go:embed docs":    true,
		"//nolint:errcheck":  true,
		"// hack: not a dir": false,
		"// regular comment": false,
		"//just text":        false,
		"//TODO:nope":        false,
	}
	for text, want := range cases {
		if got := isDirective(text); got != want {
			t.Errorf("isDirective(%q) = %v, want %v", text, got, want)
		}
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
