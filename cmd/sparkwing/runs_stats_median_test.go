package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestRunStatisticsReportsNearestRankP50(t *testing.T) {
	for _, count := range []int{0, 1, 4, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			start := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
			runs := []controllerwire.RunDetail{{ID: "running", Number: int64(count + 1), Status: "running", Inputs: runspec.Display{Pipeline: "check"}}}
			for i := count; i > 0; i-- {
				finish := start.Add(time.Duration(i) * time.Second)
				runs = append(runs, controllerwire.RunDetail{ID: fmt.Sprint(i), Number: int64(i), Status: "success", StartedAt: &start, FinishedAt: &finish, Inputs: runspec.Display{Pipeline: "check"}})
			}
			reader := listingReader{list: func(context.Context, controllerwire.RunQuery) (controllerwire.RunPage, error) {
				return controllerwire.RunPage{Runs: runs}, nil
			}}
			inv, err := clisurface.Parse("sparkwing", []string{"runs", "stats", "-o", "json"})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := observeRunStatistics(t.Context(), inv, reader, &out, start); err != nil {
				t.Fatal(err)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			median, ok := result["p50_duration_ns"]
			if !ok {
				t.Fatalf("statistics lack P50: %s", out.String())
			}
			var got time.Duration
			if err := json.Unmarshal(median, &got); err != nil {
				t.Fatal(err)
			}
			want := time.Duration((count+1)/2) * time.Second
			if got != want {
				t.Fatalf("P50 = %s, want %s", got, want)
			}
			inv, err = clisurface.Parse("sparkwing", []string{"runs", "stats", "-o", "pretty"})
			if err != nil {
				t.Fatal(err)
			}
			out.Reset()
			if err := observeRunStatistics(t.Context(), inv, reader, &out, start); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "P50") {
				t.Fatalf("table lacks P50: %s", out.String())
			}
		})
	}
}
