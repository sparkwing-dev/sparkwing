package blob_test

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
)

func TestLogObjectKeysKeepEveryIdentityDistinct(t *testing.T) {
	err := quick.Check(func(seed [16]byte, ordinal, generation uint16, index uint32) bool {
		seen := map[string]bool{}
		for _, subject := range generatedLogSubjects(seed, ordinal, generation) {
			for _, variant := range logSubjectVariants(subject) {
				if !distinctLogKeys(seen, variant, index) {
					return false
				}
			}
		}
		return true
	}, &quick.Config{MaxCount: 128})
	if err != nil {
		t.Fatal(err)
	}
}

func generatedLogSubjects(seed [16]byte, ordinal, generation uint16) []blob.LogSubject {
	team, run := fmt.Sprintf("team-%x", seed[:4]), fmt.Sprintf("run-%x", seed[4:8])
	job := fmt.Sprintf("parent/%x", seed[8:])
	attempt := runspec.AttemptIdentity{Team: team, RunID: run, NodeID: job, Attempt: int64(ordinal) + 1, Generation: int64(generation) + 1}
	return []blob.LogSubject{
		{Kind: blob.LogJobAttempt, Team: team, RunID: run, Attempt: &attempt},
		{Kind: blob.LogOwnerUnit, Team: team, RunID: run, Unit: &blob.LogUnit{Kind: blob.LogTrigger, Generation: attempt.Generation}},
		{Kind: blob.LogOwnerUnit, Team: team, RunID: run, Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: attempt.Generation}},
		{Kind: blob.LogOwnerUnit, Team: team, RunID: run, Unit: &blob.LogUnit{Kind: blob.LogEval, Generation: attempt.Generation, Source: &blob.LogSource{NodeID: job, Generation: attempt.Generation}}},
	}
}

func logSubjectVariants(subject blob.LogSubject) []blob.LogSubject {
	out := []blob.LogSubject{subject}
	for field := range 5 {
		other := subject
		if subject.Attempt != nil {
			attempt := *subject.Attempt
			other.Attempt = &attempt
		} else {
			unit := *subject.Unit
			other.Unit = &unit
			if unit.Source != nil {
				source := *unit.Source
				unit.Source = &source
			}
		}
		if changeLogSubject(&other, field) {
			out = append(out, other)
		}
	}
	return out
}

func changeLogSubject(subject *blob.LogSubject, field int) bool {
	switch field {
	case 0:
		subject.Team += "-other"
		if subject.Attempt != nil {
			subject.Attempt.Team = subject.Team
		}
	case 1:
		subject.RunID += "-other"
		if subject.Attempt != nil {
			subject.Attempt.RunID = subject.RunID
		}
	default:
		if a := subject.Attempt; a != nil {
			switch field {
			case 2:
				a.NodeID += "_2f"
			case 3:
				a.Attempt++
			case 4:
				a.Generation++
			default:
				return false
			}
		} else {
			switch field {
			case 2:
				subject.Unit.Generation++
			case 3:
				if subject.Unit.Source == nil {
					return false
				}
				subject.Unit.Source.NodeID += "_2f"
			case 4:
				if subject.Unit.Source == nil {
					return false
				}
				subject.Unit.Source.Generation++
			default:
				return false
			}
		}
	}
	return true
}

func distinctLogKeys(seen map[string]bool, subject blob.LogSubject, index uint32) bool {
	for _, class := range []string{"r30", "r90"} {
		prefix, err := blob.LogSubjectPrefix(class, subject)
		if err != nil {
			return false
		}
		for _, n := range []int64{0, 1 + int64(index%uint32(limits.MaxLogSegmentNumber))} {
			key, err := blob.LogObjectKey(subject, class, n)
			if err != nil || !fs.ValidPath(key) || !strings.HasPrefix(key, prefix) || seen[key] {
				return false
			}
			seen[key] = true
			if n == 0 {
				if key != blob.LogManifestKey(prefix) {
					return false
				}
			} else if parsed, ok := blob.LogSegmentNumber(prefix, key); !ok || int64(parsed) != n || key != blob.LogSegmentKey(prefix, int(n)) {
				return false
			}
		}
	}
	return true
}

func TestLogObjectKeysRejectInvalidSlotsAndPaths(t *testing.T) {
	attempt := runspec.AttemptIdentity{Team: "acme", RunID: "run", NodeID: "build/linux", Attempt: 2, Generation: 7}
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: attempt.Team, RunID: attempt.RunID, Attempt: &attempt}
	want := "logs/r30/teams/acme/runs/run/jobs/build_2flinux/a2-g7/seg-000001.ndjson.gz"
	if got, err := blob.LogObjectKey(subject, "r30", 1); err != nil || got != want {
		t.Fatalf("object key = %q: %v", got, err)
	}
	for _, n := range []int64{-1, int64(limits.MaxLogSegmentNumber) + 1} {
		if _, err := blob.LogObjectKey(subject, "r30", n); err == nil {
			t.Fatalf("accepted slot %d", n)
		}
	}
	for _, path := range []string{"", ".", "..", "../job", "a/../b", "a//b", "a\\b", "a\x00b"} {
		copy := attempt
		copy.NodeID = path
		subject.Attempt = &copy
		if _, err := blob.LogObjectKey(subject, "r30", 1); err == nil {
			t.Fatalf("accepted job path %q", path)
		}
	}
	for _, key := range []string{"seg-000000.ndjson.gz", "seg-0000001.ndjson.gz", fmt.Sprintf("seg-%06d.ndjson.gz", limits.MaxLogSegmentNumber+1)} {
		if _, ok := blob.LogSegmentNumber("", key); ok {
			t.Fatalf("accepted noncanonical key %q", key)
		}
	}
}
