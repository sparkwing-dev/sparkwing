package logseg

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestJobSubjectPrefixLayout(t *testing.T) {
	got, err := blob.LogSubjectPrefix(classFree, keySubject("acme", "run-1", "build/linux", 2, 7))
	if err != nil {
		t.Fatal(err)
	}
	if want := "logs/r30/teams/acme/runs/run-1/jobs/build_2flinux/a2-g7/"; got != want {
		t.Fatalf("prefix %q, want %q", got, want)
	}
	other, err := blob.LogSubjectPrefix(classFree, keySubject("acme", "run-1", "build__linux", 2, 7))
	if err != nil || other == got {
		t.Fatalf("hierarchical and flat node IDs share a prefix: %q, %v", other, err)
	}
	if escaped, err := blob.LogSubjectPrefix(classFree, keySubject("acme", "r", ".hidden/a_b c", 1, 1)); err != nil || escaped != "logs/r30/teams/acme/runs/r/jobs/_2ehidden_2fa_5fb_20c/a1-g1/" {
		t.Fatalf("escaped subject = %q: %v", escaped, err)
	}
	if key := blob.LogSegmentKey(got, 12); key != got+"seg-000012.ndjson.gz" {
		t.Fatalf("segment key %q", key)
	}
	if n, ok := blob.LogSegmentNumber(got, blob.LogSegmentKey(got, 12)); !ok || n != 12 {
		t.Fatalf("SegmentNumber = %d, %v", n, ok)
	}
	if _, ok := blob.LogSegmentNumber(got, blob.LogManifestKey(got)); ok {
		t.Fatal("the manifest parsed as a segment")
	}
}

func TestJobSubjectPrefixRefusesBadParts(t *testing.T) {
	for name, args := range map[string]struct {
		class, team, run, node string
		attempt                int64
	}{
		"class":   {"90", "acme", "r", "n", 1},
		"team":    {ClassStandard, "Acme/../x", "r", "n", 1},
		"run":     {ClassStandard, "acme", "../r", "n", 1},
		"node":    {ClassStandard, "acme", "r", "a/../b", 1},
		"attempt": {ClassStandard, "acme", "r", "n", 0},
	} {
		if _, err := blob.LogSubjectPrefix(args.class, keySubject(args.team, args.run, args.node, args.attempt, 1)); err == nil {
			t.Errorf("%s: accepted %+v", name, args)
		}
	}
	if _, err := blob.LogSubjectPrefix(ClassStandard, keySubject("_", "r", "n", 1, 1)); err != nil {
		t.Fatalf("teamless run refused: %v", err)
	}
}

func keySubject(team, runID, nodeID string, attempt, generation int64) blob.LogSubject {
	return blob.LogSubject{Kind: blob.LogJobAttempt, Team: team, RunID: runID, Attempt: &runspec.AttemptIdentity{Team: team, RunID: runID, NodeID: nodeID, Attempt: attempt, Generation: generation}}
}

func jobSubject() blob.LogSubject {
	return blob.LogSubject{Kind: blob.LogJobAttempt, Team: "acme", RunID: "run-1", Attempt: &runspec.AttemptIdentity{Team: "acme", RunID: "run-1", NodeID: "build/linux", Attempt: 2, Generation: 7}}
}

func ownerSubject(kind blob.LogUnitKind) blob.LogSubject {
	s := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "acme", RunID: "run-1", Unit: &blob.LogUnit{Kind: kind, Generation: 3}}
	if kind == blob.LogEval {
		s.Unit.Source = &blob.LogSource{NodeID: "generator", Generation: 5}
	}
	return s
}

func TestSubjectPrefixKeepsEveryFenceDistinct(t *testing.T) {
	seen := map[string]bool{}
	subjects := []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)}
	for _, base := range subjects {
		for i := range 4 {
			s := base
			if base.Attempt != nil {
				a := *base.Attempt
				s.Attempt = &a
				s.Attempt.Generation += int64(i)
			} else {
				u := *base.Unit
				s.Unit = &u
				s.Unit.Generation += int64(i)
			}
			key, err := blob.LogSubjectPrefix("r90", s)
			if err != nil || seen[key] {
				t.Fatalf("colliding prefix = %q, %v", key, err)
			}
			seen[key] = true
		}
	}
	eval := ownerSubject(blob.LogEval)
	eval.Unit.Source.Generation++
	prefix, err := blob.LogSubjectPrefix("r90", eval)
	if err != nil || seen[prefix] {
		t.Fatalf("source fence collision = %q, %v", prefix, err)
	}
}
