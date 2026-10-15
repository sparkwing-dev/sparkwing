package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type repoInfoReader struct {
	controllerwire.RunReader
	list func(controllerwire.RunQuery) (controllerwire.RunPage, error)
}

func (r repoInfoReader) ListRuns(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
	return r.list(q)
}

func TestRepoInfoReadsLatestPipelineStatesAcrossControllerPages(t *testing.T) {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	run := func(number int64, repo, pipeline, status string) controllerwire.RunDetail {
		return controllerwire.RunDetail{
			ID: status, Number: number, Status: status, StartedAt: &at,
			Inputs: runspec.Display{Pipeline: pipeline, Git: runspec.Git{Repository: runspec.Repository{Display: repo}}},
		}
	}
	var cursors []string
	reader := repoInfoReader{list: func(q controllerwire.RunQuery) (controllerwire.RunPage, error) {
		cursors = append(cursors, q.Cursor)
		if q.Limit != 500 {
			t.Fatalf("query = %+v", q)
		}
		if q.Cursor == "" {
			pending := run(5, "acme/app", "pending", "pending")
			pending.StartedAt = nil
			return controllerwire.RunPage{Runs: []controllerwire.RunDetail{pending, run(4, "acme/app", "deploy", "success"), run(3, "acme/other", "foreign", "failed")}, Truncated: true, NextCursor: "page-two"}, nil
		}
		return controllerwire.RunPage{Runs: []controllerwire.RunDetail{run(2, "acme/app", "deploy", "cancelled"), run(1, "acme/app", "test", "skipped")}}, nil
	}}
	got, err := pipelineStates(t.Context(), repos.Repo{Name: "app"}, reader)
	if err != nil || !reflect.DeepEqual(cursors, []string{"", "page-two"}) || len(got) != 3 {
		t.Fatalf("states = %+v, cursors = %v, err = %v", got, cursors, err)
	}
	if got[0].Name != "deploy" || got[0].LastStatus != "success" || got[0].LastRun != "2026-10-14 12:00" || got[1].Name != "pending" || got[1].LastRun != "" || got[1].LastStatus != "pending" || got[2].Name != "test" || got[2].LastStatus != "skipped" {
		t.Fatalf("pipeline history = %+v", got)
	}
}

func TestRepoInfoHistoryFailureIsLoud(t *testing.T) {
	want := errors.New("controller history failed")
	reader := repoInfoReader{list: func(controllerwire.RunQuery) (controllerwire.RunPage, error) {
		return controllerwire.RunPage{}, want
	}}
	if got, err := pipelineStates(t.Context(), repos.Repo{Name: "app"}, reader); got != nil || !errors.Is(err, want) {
		t.Fatalf("history failure became absence: %+v %v", got, err)
	}
}

func TestRepoSuggestion_LeadsWithGuidesThenDirty(t *testing.T) {
	behind := repoInfo{Pin: "v0.16.0", Latest: "v0.17.0", GuidesBehind: 2}
	if got := repoSuggestion(behind); !strings.Contains(got, "2 guide(s) behind") {
		t.Fatalf("behind suggestion = %q, want the guides bump", got)
	}

	dirty := repoInfo{Pin: "v0.17.0", Latest: "v0.17.0", Dirty: true}
	if got := repoSuggestion(dirty); !strings.Contains(got, "uncommitted") {
		t.Fatalf("dirty suggestion = %q, want the dirty-tree note", got)
	}

	clean := repoInfo{Pin: "v0.17.0", Latest: "v0.17.0"}
	if got := repoSuggestion(clean); got != "" {
		t.Fatalf("clean suggestion = %q, want empty", got)
	}
}
