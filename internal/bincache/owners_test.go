package bincache

import (
	"path/filepath"
	"testing"
)

// An entry is a content fingerprint and -trimpath keeps build paths out
// of the binary, so the owners record is the only thing that can say
// what a cached 90 MB blob belongs to.
func TestRecordOwner_TracksCheckoutsAndCounts(t *testing.T) {
	isolateCache(t)
	seedEntry(t, "shared", 10, 0)

	primary := filepath.Join(t.TempDir(), "primary", ".sparkwing")
	worktree := filepath.Join(t.TempDir(), "worktree", ".sparkwing")
	RecordOwner("shared", primary)
	RecordOwner("shared", primary)
	RecordOwner("shared", worktree)

	owners := Owners("shared")
	if len(owners) != 2 {
		t.Fatalf("expected both checkouts recorded, got %+v", owners)
	}
	if got := TotalUses(owners); got != 3 {
		t.Fatalf("TotalUses = %d, want 3", got)
	}
	byDir := map[string]int{}
	for _, o := range owners {
		byDir[o.Dir] = o.Uses
	}
	if byDir[primary] != 2 || byDir[worktree] != 1 {
		t.Fatalf("per-checkout counts wrong: %+v", byDir)
	}
}

func TestOwners_MissingFileIsNotAnError(t *testing.T) {
	isolateCache(t)
	if owners := Owners("never-written"); owners != nil {
		t.Fatalf("absent owners should read as nil, got %+v", owners)
	}
}

// The explanation and the key must come from one computation, or
// explain would confidently describe inputs that did not produce the
// key it prints.
func TestExplainCacheKey_AgreesWithPipelineCacheKey(t *testing.T) {
	dir := newPipelineDir(t)
	key, parts, err := ExplainCacheKey(dir)
	if err != nil {
		t.Fatalf("ExplainCacheKey: %v", err)
	}
	if want := mustKey(t, dir); key != want {
		t.Fatalf("explain key %s disagrees with PipelineCacheKey %s", key, want)
	}
	if len(parts) == 0 {
		t.Fatal("explain reported no inputs")
	}
	for _, p := range parts {
		if p.Label == "" || p.Digest == "" {
			t.Fatalf("input missing label or digest: %+v", p)
		}
	}
}

// The whole point of explain: name the input that moved.
func TestDiffKeyParts_NamesTheChangedInput(t *testing.T) {
	dir := newPipelineDir(t)
	_, before, err := ExplainCacheKey(dir)
	if err != nil {
		t.Fatalf("ExplainCacheKey: %v", err)
	}
	stored := map[string]string{}
	for _, p := range before {
		stored[p.Label] = p.Digest
	}

	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() { println(1) }\n")
	_, after, err := ExplainCacheKey(dir)
	if err != nil {
		t.Fatalf("ExplainCacheKey: %v", err)
	}

	changed := DiffKeyParts(stored, after)
	if len(changed) != 1 || changed[0] != "module tree" {
		t.Fatalf("editing the module should report exactly the module tree, got %v", changed)
	}
}
