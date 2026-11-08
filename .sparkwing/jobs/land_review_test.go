package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestLandReadsPersistedReviewWithoutOutputBlob(t *testing.T) {
	remote := newLandRemote(t)
	head := landHead{Branch: "work", Commit: remote.git(remote.dev, "rev-parse", "HEAD"), Tree: remote.git(remote.dev, "rev-parse", "HEAD^{tree}"), Base: remote.git(remote.dev, "rev-parse", "origin/main")}
	common := remote.git(remote.dev, "rev-parse", "--path-format=absolute", "--git-common-dir")
	want := reviewRecord{Version: 1, Branch: head.Branch, Tree: head.Tree, Commit: head.Commit, Result: resultPass}
	if err := saveReviewRecord(reviewRecordPath(filepath.Join(common, "sparkwing-review"), head.Branch), want); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(head)
	if err != nil {
		t.Fatal(err)
	}
	host := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"preconditions": raw}})
	job := landJob{RepoDir: remote.dev, Head: sparkwing.Ref[landHead]{JobID: "preconditions"}}
	defer func() {
		if failure := recover(); failure != nil {
			t.Fatalf("persisted review required an output blob: %v", failure)
		}
	}()
	got, ok, err := job.review(host.Context(t.Context()))
	if err != nil || !ok || got.Tree != want.Tree || got.Result != resultPass {
		t.Fatalf("persisted review: %+v present=%v", got, ok)
	}
}

func TestLandRefusesAbsentOrInvalidPersistedReview(t *testing.T) {
	remote := newLandRemote(t)
	head := landHead{Branch: "work", Tree: remote.git(remote.dev, "rev-parse", "HEAD^{tree}")}
	common := remote.git(remote.dev, "rev-parse", "--path-format=absolute", "--git-common-dir")
	file := reviewRecordPath(filepath.Join(common, "sparkwing-review"), head.Branch)
	raw, err := json.Marshal(head)
	if err != nil {
		t.Fatal(err)
	}
	host := newTestHost(t, sparkwingtest.Options{Outputs: map[string]json.RawMessage{"preconditions": raw}})
	job := landJob{RepoDir: remote.dev, Head: sparkwing.Ref[landHead]{JobID: "preconditions"}}
	for _, record := range []*reviewRecord{nil, {Version: 1, Branch: "another", Tree: head.Tree, Result: resultPass}} {
		if record != nil {
			if err := saveReviewRecord(file, *record); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := job.review(host.Context(t.Context())); err == nil {
			t.Fatal("land accepted missing or foreign review evidence")
		}
	}
	if err := os.WriteFile(file, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := job.review(host.Context(t.Context())); err == nil {
		t.Fatal("land accepted malformed review evidence")
	}
}
