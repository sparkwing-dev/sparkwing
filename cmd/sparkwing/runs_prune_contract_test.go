package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

type pruneController struct {
	listingReader
	deletes []string
	failure error
}

func (c *pruneController) DeleteRun(_ context.Context, id string) error {
	c.deletes = append(c.deletes, id)
	return c.failure
}

func pruneInvocation(t *testing.T, args ...string) clisurface.Invocation {
	t.Helper()
	inv, err := clisurface.Parse("sparkwing", append([]string{"runs", "prune"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestRunPruneDrainsPagesBeforeDeletion(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	c := &pruneController{}
	reads := 0
	c.list = func(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
		if len(c.deletes) > 0 {
			t.Fatal("deletion changed selection while pages were still being read")
		}
		reads++
		if !q.StartedBefore.Equal(now.Add(-24 * time.Hour)) {
			t.Fatalf("cutoff lost: %+v", q)
		}
		run := listedRun("older", 1)
		run.StartedAt = &old
		if q.Cursor != "" {
			return controllerwire.RunPage{Runs: []controllerwire.RunDetail{run}}, nil
		}
		run = listedRun("first", 3)
		run.StartedAt = &old
		active := listedRun("running", 2)
		active.Status = "running"
		active.StartedAt = &old
		return controllerwire.RunPage{Runs: []controllerwire.RunDetail{run, active}, NextCursor: "page2", Truncated: true}, nil
	}
	var out bytes.Buffer
	err := pruneObservedRuns(t.Context(), pruneInvocation(t, "--older-than", "1d", "-o", "plain"), c, nil, &out, now)
	if err != nil || reads != 2 || !reflect.DeepEqual(c.deletes, []string{"first", "older"}) || out.String() != "first\nolder\n" {
		t.Fatalf("prune: %v reads=%d deletes=%v out=%q", err, reads, c.deletes, out.String())
	}
}

func TestRunPruneSelectionFailureDoesNotDelete(t *testing.T) {
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	sentinel := errors.New("next page unavailable")
	c := &pruneController{}
	c.list = func(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
		if q.Cursor != "" {
			return controllerwire.RunPage{}, sentinel
		}
		run := listedRun("r1", 1)
		run.StartedAt = &old
		return controllerwire.RunPage{Runs: []controllerwire.RunDetail{run}, NextCursor: "next", Truncated: true}, nil
	}
	err := pruneObservedRuns(t.Context(), pruneInvocation(t, "--older-than", "1d"), c, nil, &bytes.Buffer{}, now)
	if !errors.Is(err, sentinel) || len(c.deletes) > 0 {
		t.Fatalf("partial selection pruned: %v %v", err, c.deletes)
	}
}

func TestRunPruneDryRunAndStructuredFailure(t *testing.T) {
	var out bytes.Buffer
	c := &pruneController{}
	err := pruneObservedRuns(t.Context(), pruneInvocation(t, "--run", "-", "--dry-run", "-o", "json"), c, strings.NewReader("42\n42\n43\n"), &out, time.Now())
	if err != nil || len(c.deletes) != 0 || strings.Count(out.String(), `"dry_run":true`) != 2 {
		t.Fatalf("dry-run: %v %v %s", err, c.deletes, &out)
	}
	out.Reset()
	failure := &apierr.Error{Code: apierr.Conflict, Message: "retained newest success", Hint: "publish a newer success first"}
	c.failure = failure
	err = pruneObservedRuns(t.Context(), pruneInvocation(t, "--run", "42", "-o", "json"), c, nil, &out, time.Now())
	if !errors.Is(err, failure) || !strings.Contains(out.String(), `"code":"conflict"`) || !strings.Contains(out.String(), `"hint":"publish a newer success first"`) {
		t.Fatalf("refusal lost: %v %s", err, &out)
	}
}

func TestRunPruneReportsOnlyAcknowledgedSelection(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			err := pruneObservedRuns(t.Context(), pruneInvocation(t, "--run", "42", "-o", format), &pruneController{}, nil, &out, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"json": "{\"run_id\":\"42\"}\n", "plain": "42\n", "pretty": "42: pruned\n"}[format]
			if out.String() != want {
				t.Fatalf("prune output: %q want %q", out.String(), want)
			}
		})
	}
}
