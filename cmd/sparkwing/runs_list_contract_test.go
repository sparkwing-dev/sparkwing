package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type listingReader struct {
	controllerwire.RunReader
	list  func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error)
	steps func(context.Context, string) (controllerwire.RunSteps, error)
}

func (r listingReader) ListRuns(ctx context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
	return r.list(ctx, q)
}
func (r listingReader) ReadSteps(ctx context.Context, id string) (controllerwire.RunSteps, error) {
	return r.steps(ctx, id)
}

func listingInvocation(t *testing.T, args ...string) clisurface.Invocation {
	t.Helper()
	inv, err := clisurface.Parse("sparkwing", append([]string{"runs", "list"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}
func listedRun(id string, number int64) controllerwire.RunDetail {
	return controllerwire.RunDetail{ID: id, Number: number, Team: "acme", Status: "success", Inputs: runspec.Display{Pipeline: "check"}}
}

func TestRunListingPreservesFiltersAndPage(t *testing.T) {
	inv := listingInvocation(t, "--repo", "acme/api,acme/web", "--root-only", "--branch", "main,!release", "--error", "permission", "--sha", " abc123 ", "--since", "7d", "--cursor", "opaque", "--limit", "1", "-o", "json")
	var out bytes.Buffer
	reader := listingReader{list: func(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
		if !reflect.DeepEqual(q.Repos, []string{"acme/api", "acme/web"}) || !reflect.DeepEqual(q.Branches, []string{"main", "!release"}) || !q.RootOnly || q.Error != "permission" || q.SHA != "abc123" || q.StartedAfter.IsZero() || q.Cursor != "opaque" || q.Limit != 1 {
			t.Fatalf("filters lost: %+v", q)
		}
		return controllerwire.RunPage{Runs: []controllerwire.RunDetail{listedRun("older-matching-run", 42)}, NextCursor: "next", Truncated: true}, nil
	}}
	if err := observeRunList(t.Context(), inv, reader, &out, &bytes.Buffer{}, nil); err != nil {
		t.Fatal(err)
	}
	var run controllerwire.RunDetail
	decoder := json.NewDecoder(&out)
	if err := decoder.Decode(&run); err != nil || run.ID != "older-matching-run" {
		t.Fatalf("run: %+v %v", run, err)
	}
	var meta map[string]any
	if err := decoder.Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta["next_cursor"] != "next" || meta["truncated"] != true || meta["kind"] != "page" {
		t.Fatalf("page: %+v", meta)
	}
}

func TestRunListingPivotIncludesEveryPage(t *testing.T) {
	reader := listingReader{list: func(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
		if q.Cursor == "" {
			return controllerwire.RunPage{Runs: []controllerwire.RunDetail{listedRun("new", 2)}, NextCursor: "older", Truncated: true}, nil
		}
		run := listedRun("old", 1)
		run.Status = "failed"
		return controllerwire.RunPage{Runs: []controllerwire.RunDetail{run}}, nil
	}}
	var out bytes.Buffer
	err := observeRunList(t.Context(), listingInvocation(t, "--by-pipeline", "--limit", "1", "--sparkline", "1", "-o", "json"), reader, &out, &bytes.Buffer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		Total, Failures int
		RecentStatuses  []string `json:"recent_statuses"`
	}
	if err := json.NewDecoder(&out).Decode(&row); err != nil || row.Total != 2 || row.Failures != 1 || !reflect.DeepEqual(row.RecentStatuses, []string{"success"}) {
		t.Fatalf("pivot: %+v %v", row, err)
	}
}

func TestRunListingKeepsExecutionDetailOutOfList(t *testing.T) {
	run := listedRun("r1", 1)
	run.Inputs.Args = map[string]string{"argument": "large-value"}
	run.Jobs = []controllerwire.JobDetail{{ID: "job-detail"}}
	var out bytes.Buffer
	if err := writeListedRun(&out, run, "json", false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "large-value") || strings.Contains(out.String(), "job-detail") || strings.Contains(out.String(), "inputs") {
		t.Fatalf("listing serialized detail: %s", &out)
	}
	if !strings.Contains(out.String(), `"pipeline":"check"`) {
		t.Fatalf("listing lost selection metadata: %s", &out)
	}
}

func TestRunListingRejectsRepeatedPagination(t *testing.T) {
	for _, repeat := range []string{"identity", "cursor"} {
		t.Run(repeat, func(t *testing.T) {
			read := 0
			reader := listingReader{list: func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
				read++
				id := int64(4 - read)
				cursor := "cursor"
				if repeat == "identity" {
					id = 3
					cursor = "different"
				}
				return controllerwire.RunPage{Runs: []controllerwire.RunDetail{listedRun(fmt.Sprint(id), id)}, NextCursor: cursor, Truncated: true}, nil
			}}
			if err := visitRunPages(t.Context(), reader, controllerwire.RunQuery{Limit: 1}, func(controllerwire.RunPage) error { return nil }); err == nil || read != 2 {
				t.Fatalf("pagination: %v reads=%d", err, read)
			}
		})
	}
}

func TestRunListingFollowDrainsEveryNewPage(t *testing.T) {
	inv := listingInvocation(t, "--limit", "2", "--follow", "-o", "plain")
	reads := 0
	reader := listingReader{list: func(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
		reads++
		switch reads {
		case 1:
			return controllerwire.RunPage{Runs: []controllerwire.RunDetail{listedRun("r40", 40)}}, nil
		case 2:
			if q.AfterNumber != 40 || q.Cursor != "" {
				t.Fatalf("poll query: %+v", q)
			}
			return controllerwire.RunPage{Runs: []controllerwire.RunDetail{listedRun("r43", 43), listedRun("r42", 42)}, NextCursor: "next", Truncated: true}, nil
		case 3:
			if q.AfterNumber != 40 || q.Cursor != "next" {
				t.Fatalf("page query: %+v", q)
			}
			return controllerwire.RunPage{Runs: []controllerwire.RunDetail{listedRun("r41", 41)}}, nil
		default:
			t.Fatalf("unexpected read %d", reads)
			return controllerwire.RunPage{}, nil
		}
	}}
	waits := 0
	wait := func(context.Context, time.Duration) error {
		waits++
		if waits == 1 {
			return nil
		}
		return context.Canceled
	}
	var out bytes.Buffer
	err := observeRunList(t.Context(), inv, reader, &out, &bytes.Buffer{}, wait)
	if !errors.Is(err, context.Canceled) || exitCodeFor(err) != 3 {
		t.Fatalf("follow: %v", err)
	}
	if out.String() != "r40\nr43\nr42\nr41\n" {
		t.Fatalf("lost burst: %q", out.String())
	}
}

func TestRunListingWaitAndFailuresAreLoud(t *testing.T) {
	inv := listingInvocation(t, "--wait", "--wait-timeout", "1h", "-o", "plain")
	reads := 0
	reader := listingReader{list: func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
		reads++
		return controllerwire.RunPage{}, nil
	}}
	err := observeRunList(t.Context(), inv, reader, &bytes.Buffer{}, &bytes.Buffer{}, func(context.Context, time.Duration) error { return context.DeadlineExceeded })
	if exitCodeFor(err) != 3 || reads != 1 {
		t.Fatalf("wait: %v reads=%d", err, reads)
	}
	sentinel := errors.New("controller unavailable")
	reader.list = func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
		return controllerwire.RunPage{}, sentinel
	}
	if err := observeRunList(t.Context(), inv, reader, &bytes.Buffer{}, &bytes.Buffer{}, nil); !errors.Is(err, sentinel) {
		t.Fatalf("hidden read error: %v", err)
	}
}

func TestRunListingFollowFindsEarlierRunAfterItMatches(t *testing.T) {
	reads := 0
	reader := listingReader{list: func(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
		reads++
		if reads == 1 {
			return controllerwire.RunPage{Runs: []controllerwire.RunDetail{listedRun("r40", 40)}}, nil
		}
		if q.AfterNumber != 40 {
			t.Fatalf("filter boundary advanced past pending runs: %+v", q)
		}
		runs := []controllerwire.RunDetail{listedRun("r42", 42)}
		if reads == 3 {
			runs = append(runs, listedRun("r41", 41))
		}
		return controllerwire.RunPage{Runs: runs}, nil
	}}
	waits := 0
	var out bytes.Buffer
	err := observeRunList(t.Context(), listingInvocation(t, "--status", "failed", "--follow", "-o", "plain"), reader, &out, &bytes.Buffer{}, func(context.Context, time.Duration) error {
		waits++
		if waits < 3 {
			return nil
		}
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || out.String() != "r40\nr42\nr41\n" {
		t.Fatalf("follow omitted or repeated a run: %v %q", err, out.String())
	}
}

func TestRunListingInterruptedReadExitsIncomplete(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded} {
		reader := listingReader{list: func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
			return controllerwire.RunPage{}, failure
		}}
		err := observeRunList(t.Context(), listingInvocation(t, "--wait"), reader, &bytes.Buffer{}, &bytes.Buffer{}, nil)
		if !errors.Is(err, failure) || exitCodeFor(err) != 3 {
			t.Fatalf("interrupted read: %v", err)
		}
	}
}

func TestRunListingValidatesViewsBeforeRead(t *testing.T) {
	for _, args := range [][]string{{"--group-by", "step"}, {"--status", "failed", "--group-by", "node"}, {"--style", "bad"}, {"--sparkline", "0"}, {"--wait-timeout", "0s"}, {"--since", "-1h"}} {
		inv := listingInvocation(t, args...)
		reader := listingReader{list: func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
			t.Fatal("invalid view read controller")
			return controllerwire.RunPage{}, nil
		}}
		if err := observeRunList(t.Context(), inv, reader, &bytes.Buffer{}, &bytes.Buffer{}, nil); exitCodeFor(err) != 2 {
			t.Fatalf("args %v: %v", args, err)
		}
	}
}

func TestRunListingQuietJSONAndJobStepGrouping(t *testing.T) {
	run := listedRun("r1", 1)
	run.Status = "failed"
	run.Jobs = []controllerwire.JobDetail{{ID: "build", Status: "finished", Outcome: "failed", StatusDetail: "permission denied"}}
	reader := listingReader{list: func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
		return controllerwire.RunPage{Runs: []controllerwire.RunDetail{run}}, nil
	}, steps: func(context.Context, string) (controllerwire.RunSteps, error) {
		return controllerwire.RunSteps{Steps: []controllerwire.StepDetail{{JobID: "build", ID: "compile", Attempt: 2, Status: "finished", Outcome: "failed", Reason: "compiler failed"}}}, nil
	}}
	var out bytes.Buffer
	if err := observeRunList(t.Context(), listingInvocation(t, "-q", "-o", "json"), reader, &out, &bytes.Buffer{}, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "{\"id\":\"r1\"}\n" {
		t.Fatalf("quiet: %q", out.String())
	}
	for _, group := range []string{"job", "step"} {
		out.Reset()
		if err := observeRunList(t.Context(), listingInvocation(t, "--status", "failed", "--group-by", group, "-o", "json"), reader, &out, &bytes.Buffer{}, nil); err != nil {
			t.Fatal(err)
		}
		key := "build"
		if group == "step" {
			key = "build/compile"
		}
		if !strings.Contains(out.String(), `"key":"`+key+`"`) {
			t.Fatalf("group %s: %s", group, out.String())
		}
	}
}
