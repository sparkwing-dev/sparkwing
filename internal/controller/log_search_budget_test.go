package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func budgetedLogSegments(t *testing.T) (segmentLogs, *store.Store, []string, int64) {
	t.Helper()
	var payloads [][]byte
	var want []string
	for segment := range 4 {
		var b strings.Builder
		for line := range 20 {
			text := fmt.Sprintf("fatal %d.%02d", segment, line)
			want = append(want, text)
			b.WriteString(text + "\nordinary\n")
		}
		payloads = append(payloads, []byte(b.String()))
	}
	logs, _, st := committedDashboardLogSegments(t, payloads...)
	return logs, st, want, int64(len(payloads[0]))
}

func withSearchBounds(t *testing.T, bytes int64, timeout time.Duration) {
	t.Helper()
	previous := controllerSearchBounds
	controllerSearchBounds.MaxBytes, controllerSearchBounds.Timeout = bytes, timeout
	t.Cleanup(func() { controllerSearchBounds = previous })
}

func TestJSONLogSearchStopsAtByteBudgetAndResumes(t *testing.T) {
	logs, st, want, segment := budgetedLogSegments(t)
	withSearchBounds(t, segment+segment/2, time.Minute)
	reader, _ := memberLogSearchReader(t, st, logs.objs.(blob.ObjectBackend))
	query := controllerwire.LogSearchQuery{Runs: controllerwire.RunQuery{Limit: 1}, Pattern: "fatal"}
	var got []string
	pages := 0
	for {
		page, err := reader.SearchLogs(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, m := range page.Matches {
			got = append(got, m.Line)
		}
		if !page.Truncated {
			break
		}
		if page.NextCursor == "" || pages > 8 {
			t.Fatalf("budgeted page %d gave no continuation: %+v", pages, page)
		}
		query.Runs.Cursor = page.NextCursor
	}
	if pages < 3 || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%d pages returned %v; want %v", pages, got, want)
	}
}

func TestJSONLogReadStopsAtByteBudgetAndResumes(t *testing.T) {
	logs, st, want, segment := budgetedLogSegments(t)
	withSearchBounds(t, segment+segment/2, time.Minute)
	reader, _ := memberLogSearchReader(t, st, logs.objs.(blob.ObjectBackend))
	for _, tail := range []*int{nil, new(int)} {
		query := controllerwire.LogQuery{Grep: "fatal", NoEvents: true}
		expected := want
		if tail != nil {
			*tail = 50
			query.Tail = tail
			expected = want[len(want)-25:]
		}
		var got []string
		for pages := 1; ; pages++ {
			page, err := reader.ReadLogs(t.Context(), "run", query)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range page.Records {
				got = append(got, r.Line)
			}
			if !page.Truncated {
				break
			}
			if pages > 12 {
				t.Fatalf("budgeted read did not finish: %+v", page)
			}
			query.Cursor = page.NextCursor
		}
		if strings.Join(got, ",") != strings.Join(expected, ",") {
			t.Fatalf("tail %v: read %v; want %v", tail, got, expected)
		}
	}
}

func TestJSONLogSearchStopsAtTimeBudget(t *testing.T) {
	logs, st, _, _ := budgetedLogSegments(t)
	withSearchBounds(t, 1<<30, time.Nanosecond)
	reader, _ := memberLogSearchReader(t, st, logs.objs.(blob.ObjectBackend))
	page, err := reader.SearchLogs(t.Context(), controllerwire.LogSearchQuery{Runs: controllerwire.RunQuery{Limit: 1}, Pattern: "fatal"})
	if err != nil || !page.Truncated || page.NextCursor == "" {
		t.Fatalf("expired search = %+v, %v", page, err)
	}
	read, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{Grep: "fatal", NoEvents: true})
	if err != nil || !read.Truncated || read.NextCursor == "" {
		t.Fatalf("expired read = %+v, %v", read, err)
	}
}

func TestJSONLogScansTakeTheDownloadSlotFirst(t *testing.T) {
	logs, st, _, _ := budgetedLogSegments(t)
	options, _, server := memberObservationOptions(t, st, logs.objs.(blob.ObjectBackend))
	server.WithEgressMeter(egress.New(egress.Config{MaxDownloadsPerPrincipal: 1}))
	release, err := server.egress.Open(egress.TeamPrincipal("a", "member"), egress.SlotDownload)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	reader, err := client.NewLogReader(options)
	if err != nil {
		t.Fatal(err)
	}
	var refused *apierr.Error
	if _, err := reader.SearchLogs(t.Context(), controllerwire.LogSearchQuery{Runs: controllerwire.RunQuery{Limit: 1}, Pattern: "fatal"}); !errors.As(err, &refused) || refused.Code != apierr.RateLimited {
		t.Fatalf("search with the slot held = %v", err)
	}
	if _, err := reader.ReadLogs(t.Context(), "run", controllerwire.LogQuery{Grep: "fatal"}); !errors.As(err, &refused) || refused.Code != apierr.RateLimited {
		t.Fatalf("read with the slot held = %v", err)
	}
}

func TestRunSelectionCapsLimitAtRunListMaximum(t *testing.T) {
	r, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/runs/grep?limit=1000000", nil)
	if err != nil {
		t.Fatal(err)
	}
	q, err := parseRunSelection(r)
	if err != nil || q.Limit != store.MaxRunListLimit {
		t.Fatalf("limit = %d, %v", q.Limit, err)
	}
}

func TestTextLogSearchesTakeTheDownloadSlotFirst(t *testing.T) {
	logs, st, _, _ := budgetedLogSegments(t)
	options, _, server := memberObservationOptions(t, st, logs.objs.(blob.ObjectBackend))
	server.WithEgressMeter(egress.New(egress.Config{MaxDownloadsPerPrincipal: 1}))
	bearer, err := options.Bearer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/runs/run/logs/search?q=fatal", "/api/v1/runs/grep?q=fatal"} {
		search := func() int {
			r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://local"+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+bearer)
			response, err := options.HTTP.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			return response.StatusCode
		}
		if code := search(); code != http.StatusOK {
			t.Fatalf("%s with a free slot = %d", path, code)
		}
		release, err := server.egress.Open(egress.TeamPrincipal("a", "member"), egress.SlotDownload)
		if err != nil {
			t.Fatal(err)
		}
		code := search()
		release()
		if code != http.StatusTooManyRequests {
			t.Fatalf("%s with the slot held = %d", path, code)
		}
	}
}
