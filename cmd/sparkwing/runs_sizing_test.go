package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type sizingManager struct {
	list    func(controllerwire.SizingQuery) (controllerwire.SizingPage, error)
	reset   controllerwire.SizingReset
	summary controllerwire.SizingResetSummary
	err     error
}

func (s *sizingManager) ListSizing(_ context.Context, q controllerwire.SizingQuery) (controllerwire.SizingPage, error) {
	return s.list(q)
}
func (s *sizingManager) ResetSizing(_ context.Context, input controllerwire.SizingReset) (controllerwire.SizingResetSummary, error) {
	s.reset = input
	return s.summary, s.err
}

func TestSizingPreservesControllerMeasurementsAcrossPages(t *testing.T) {
	measurements := controllerwire.SizingMeasurements{CPUP50: 0.5, CPUP95: 1.25, PeakCores: 2, SustainedCores: 1.5, MemoryP50Bytes: 128 << 20, MemoryP95Bytes: 256 << 20, PeakMemoryBytes: 1 << 30, WaitP50: 4 * time.Second, WaitP99: 2 * time.Minute, WaitSampleCount: 9, SampleCount: 10, CPUMeasured: true, FloorCores: 3, PinnedCores: 1, ContendedCount: 2, PrevPeakCores: 7}
	row := controllerwire.SizingDetail{Repo: "acme/api", Pipeline: "check", Source: "floor", Drift: "pin below measured demand", MemoExcluded: 5, Rollup: measurements, Jobs: []controllerwire.JobSizing{{JobID: "compile", SizingMeasurements: measurements}}}
	s := &sizingManager{list: func(q controllerwire.SizingQuery) (controllerwire.SizingPage, error) {
		if q.Pipeline != "check" {
			t.Fatalf("filter lost: %+v", q)
		}
		if q.Cursor == "" {
			return controllerwire.SizingPage{Sizing: []controllerwire.SizingDetail{row}, Truncated: true, NextCursor: "next"}, nil
		}
		other := row
		other.Repo = "acme/web"
		return controllerwire.SizingPage{Sizing: []controllerwire.SizingDetail{other}}, nil
	}}
	for _, format := range []string{"json", "plain", "pretty"} {
		inv, err := clisurface.Parse("sparkwing", []string{"runs", "stats", "--sizing", "--pipeline", "check", "-o", format})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := observeSizing(t.Context(), inv, s, &out); err != nil {
			t.Fatal(err)
		}
		if format == "json" {
			var got controllerwire.SizingDetail
			if err := json.NewDecoder(&out).Decode(&got); err != nil || !reflect.DeepEqual(got, row) {
				t.Fatalf("measurements lost: %+v %v", got, err)
			}
		} else if format == "plain" && out.String() != "acme/api/check\nacme/web/check\n" {
			t.Fatalf("plain: %q", out.String())
		} else if format == "pretty" {
			for _, want := range []string{"0.5/1.2/2.0", "128.0 MiB/256.0 MiB/1.0 GiB", "4.0s/2m0s", "pin below measured demand", "compile", "floor"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q: %s", want, out.String())
				}
			}
		}
	}
}

func TestSizingResetRetainsSummaryAndRefusals(t *testing.T) {
	for _, args := range [][]string{{"--pipeline", "check"}, {"--all-pipelines", "--yes"}} {
		inv, err := clisurface.Parse("sparkwing", append([]string{"runs", "stats", "--reset", "-o", "json"}, args...))
		if err != nil {
			t.Fatal(err)
		}
		s := &sizingManager{summary: controllerwire.SizingResetSummary{Pipelines: []string{"acme/api/check"}, RowsDeleted: 2, RowsCleared: 1, SamplesDropped: 3, FloorsDropped: 1}}
		var out bytes.Buffer
		if err := observeSizing(t.Context(), inv, s, &out); err != nil {
			t.Fatal(err)
		}
		if s.reset.AllPipelines != inv.Bool("all-pipelines") || s.reset.Pipeline != inv.Value("pipeline") {
			t.Fatalf("reset selection: %+v", s.reset)
		}
		var got controllerwire.SizingResetSummary
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, s.summary) {
			t.Fatalf("summary: %+v %v", got, err)
		}
		out.Reset()
		s.err = errors.New("reset refused")
		if err := observeSizing(t.Context(), inv, s, &out); !errors.Is(err, s.err) || out.Len() != 0 {
			t.Fatalf("reset failure: %s %v", out.String(), err)
		}
	}
	var out bytes.Buffer
	if err := writeSizingReset(&out, controllerwire.SizingResetSummary{RowsCleared: 1, FloorsDropped: 1}, "pretty"); err != nil || !strings.Contains(out.String(), "1 demand floor(s)") || !strings.Contains(out.String(), "pins retained") {
		t.Fatalf("floor reset: %s %v", out.String(), err)
	}
}

func TestSizingIncompletePageEmitsNothing(t *testing.T) {
	inv, _ := clisurface.Parse("sparkwing", []string{"runs", "stats", "--sizing", "-o", "json"})
	s := &sizingManager{list: func(q controllerwire.SizingQuery) (controllerwire.SizingPage, error) {
		return controllerwire.SizingPage{Sizing: []controllerwire.SizingDetail{{Pipeline: "check", Source: "measured"}}, Truncated: true, NextCursor: "repeat"}, nil
	}}
	var out bytes.Buffer
	if err := observeSizing(t.Context(), inv, s, &out); err == nil || out.Len() != 0 {
		t.Fatalf("repeated cursor accepted: %s %v", out.String(), err)
	}
}
