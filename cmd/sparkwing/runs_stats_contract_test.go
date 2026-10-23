package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestRunStatisticsConsumesEveryFilteredPage(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	end := now.Add(time.Minute)
	calls := 0
	reader := listingReader{list: func(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
		calls++
		if !q.RootOnly || q.Pipeline != "check" || !q.StartedAfter.Equal(now.Add(-7*24*time.Hour)) {
			t.Fatalf("filters: %+v", q)
		}
		run := controllerwire.RunDetail{ID: "r1", Number: 2, Status: "success", StartedAt: &now, FinishedAt: &end}
		run.Inputs.Pipeline = "check"
		if calls == 1 {
			return controllerwire.RunPage{Runs: []controllerwire.RunDetail{run}, Truncated: true, NextCursor: "next"}, nil
		}
		if q.Cursor != "next" {
			t.Fatalf("cursor: %+v", q)
		}
		run.ID, run.Number, run.Status = "r2", 1, "cancelled"
		return controllerwire.RunPage{Runs: []controllerwire.RunDetail{run}}, nil
	}}
	inv, err := clisurface.Parse("sparkwing", []string{"runs", "stats", "--pipeline", "check", "--since", "7d", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := observeRunStatistics(t.Context(), inv, reader, &out, now); err != nil {
		t.Fatal(err)
	}
	var got pipelineStats
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || got.Runs != 2 || got.Passed != 1 || got.Cancelled != 1 || got.Running != 0 || got.SuccessPct != 50 || got.AvgDur != time.Minute {
		t.Fatalf("statistics: %+v calls=%d", got, calls)
	}
}

func TestRunStatisticsIsAtomicOnIncompleteSelection(t *testing.T) {
	calls := 0
	reader := listingReader{list: func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
		calls++
		if calls == 1 {
			return controllerwire.RunPage{Runs: []controllerwire.RunDetail{{ID: "r1", Number: 2, Status: "success", Inputs: runspec.Display{Pipeline: "check"}}}, Truncated: true, NextCursor: "next"}, nil
		}
		return controllerwire.RunPage{}, errors.New("store unavailable")
	}}
	inv, _ := clisurface.Parse("sparkwing", []string{"runs", "stats", "-o", "json"})
	var out bytes.Buffer
	if err := observeRunStatistics(t.Context(), inv, reader, &out, time.Now()); err == nil || out.Len() != 0 {
		t.Fatalf("partial result: %s %v", out.String(), err)
	}
}

func TestRunStatisticsStatesAndPercentile(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	var runs []controllerwire.RunDetail
	for i := 1; i <= 20; i++ {
		end := now.Add(time.Duration(i) * time.Second)
		runs = append(runs, controllerwire.RunDetail{Status: "success", StartedAt: &now, FinishedAt: &end})
	}
	runs = append(runs, controllerwire.RunDetail{Status: "queued"}, controllerwire.RunDetail{Status: "failed"}, controllerwire.RunDetail{Status: "cancelled"})
	got, err := aggregateRunDetails("check", runs)
	if err != nil || got.Runs != 23 || got.Running != 1 || got.Failed != 1 || got.Cancelled != 1 || got.P95Dur != 19*time.Second || got.AvgDur != 10500*time.Millisecond {
		t.Fatalf("statistics: %+v %v", got, err)
	}
	bad := now.Add(-time.Second)
	_, err = aggregateRunDetails("check", []controllerwire.RunDetail{{Status: "success", StartedAt: &now, FinishedAt: &bad}})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.ProtocolError {
		t.Fatalf("negative duration accepted: %v", err)
	}
}

func TestRunStatisticsPlainAndInvalidModes(t *testing.T) {
	for _, args := range [][]string{{"--reset"}, {"--all-pipelines"}, {"--yes"}, {"--reset", "--pipeline", "check", "--all-pipelines", "--yes"}, {"--reset", "--all-pipelines"}, {"--since", "-1d"}, {"--sizing", "--since", "7d"}} {
		inv, err := clisurface.Parse("sparkwing", append([]string{"runs", "stats"}, args...))
		if err == nil {
			_, err = parseRunStatistics(inv, time.Now())
		}
		if err == nil {
			t.Fatalf("invalid mode accepted: %v", args)
		}
	}
	inv, _ := clisurface.Parse("sparkwing", []string{"runs", "stats", "-o", "plain"})
	reader := listingReader{list: func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
		return controllerwire.RunPage{Runs: []controllerwire.RunDetail{{ID: "r1", Number: 1, Status: "success", Inputs: runspec.Display{Pipeline: "check"}}}}, nil
	}}
	var out bytes.Buffer
	if err := observeRunStatistics(t.Context(), inv, reader, &out, time.Now()); err != nil || strings.TrimSpace(out.String()) != "check" {
		t.Fatalf("plain: %q %v", out.String(), err)
	}
}
