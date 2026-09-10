package opsview_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func etaFixtureNow() time.Time {
	return time.Date(2026, 9, 10, 9, 0, 0, 0, time.Local)
}

// etaFixture carries one measured and one unmeasured row on each side, so a
// renderer that fabricates an estimate is visible in the same output as one
// that drops a measured one.
func etaFixture() wingwire.QueueState {
	start := int64(90_000)
	return wingwire.QueueState{
		Resources: []wingwire.ResourceState{{Key: "cores", Capacity: 16, Held: 4, Available: 12}},
		Holders: []wingwire.Holder{
			{
				RunID:              "hold-profiled",
				Pipeline:           "pre-commit",
				Repo:               "sparkwing",
				ElapsedMS:          60_000,
				ExpectedDurationMS: 300_000,
				Resources:          wingwire.HostResources{Cores: 4},
				CostSource:         "measured",
			},
			{
				RunID:     "hold-cold",
				Pipeline:  "lint",
				Repo:      "overwing",
				ElapsedMS: 30_000,
				Resources: wingwire.HostResources{Cores: 2},
			},
		},
		Waiters: []wingwire.Waiter{
			{
				RunID:              "queue-profiled",
				Pipeline:           "test",
				Repo:               "sparkwing",
				Position:           1,
				Priority:           0,
				WaitingMS:          15_000,
				ExpectedDurationMS: 120_000,
				ExpectedStartMS:    &start,
				Resources:          wingwire.HostResources{Cores: 8},
				CostSource:         "measured",
			},
			{
				RunID:     "queue-cold",
				Pipeline:  "docs",
				Repo:      "bitwing",
				Position:  2,
				WaitingMS: 5_000,
				Resources: wingwire.HostResources{Cores: 1},
			},
		},
	}
}

func TestRenderQueueAt_PrettyShowsRemainingAndFinish(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderQueueAt(&buf, etaFixture(), "pretty", etaFixtureNow()); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"\nRunning\n", "\nQueued\n", "REMAINING", "FINISH", "STARTS IN"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pretty queue omits %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "1 unmeasured") {
		t.Fatalf("header omits the unmeasured waiter count:\n%s", out)
	}
	holder := lineWith(t, out, "hold-profiled")
	if !strings.Contains(holder, "4m0s") {
		t.Fatalf("measured holder omits its 4m0s remaining: %q", holder)
	}
	if !strings.Contains(holder, "09:04:00") {
		t.Fatalf("measured holder omits its 09:04:00 finish: %q", holder)
	}
	if cold := lineWith(t, out, "hold-cold"); strings.Count(cold, "unmeasured") != 2 {
		t.Fatalf("holder with no profile must say unmeasured for both remaining and finish: %q", cold)
	}
	waiter := lineWith(t, out, "queue-profiled")
	if !strings.Contains(waiter, "1m30s") {
		t.Fatalf("measured waiter omits its 1m30s start: %q", waiter)
	}
	if !strings.Contains(waiter, "09:03:30") {
		t.Fatalf("measured waiter omits its 09:03:30 finish: %q", waiter)
	}
	coldWaiter := lineWith(t, out, "queue-cold")
	if !strings.Contains(coldWaiter, "unknown") || !strings.Contains(coldWaiter, "unmeasured") {
		t.Fatalf("waiter with no profile must say why each cell is empty: %q", coldWaiter)
	}
}

func TestRenderQueueAt_PrettySaysPastP50WhenAHolderOutlivesItsProfile(t *testing.T) {
	qs := etaFixture()
	qs.Holders[0].ElapsedMS = 600_000
	var buf bytes.Buffer
	if err := opsview.RenderQueueAt(&buf, qs, "pretty", etaFixtureNow()); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	line := lineWith(t, buf.String(), "hold-profiled")
	if !strings.Contains(line, "past p50") {
		t.Fatalf("holder past its p50 must say so rather than estimate: %q", line)
	}
}

func TestRenderQueueAt_PrettySaysUnknownWhenNoStartCanBeProjected(t *testing.T) {
	qs := etaFixture()
	qs.Waiters[0].ExpectedStartMS = nil
	var buf bytes.Buffer
	if err := opsview.RenderQueueAt(&buf, qs, "pretty", etaFixtureNow()); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	out := buf.String()
	line := lineWith(t, out, "queue-profiled")
	if !strings.Contains(line, "unknown") {
		t.Fatalf("a measured waiter the daemon cannot place must say unknown: %q", line)
	}
	if strings.Contains(line, "unmeasured") {
		t.Fatalf("a measured waiter must not be called unmeasured: %q", line)
	}
	if !strings.Contains(out, "(1 unmeasured)") {
		t.Fatalf("header counts only the waiters with no profile of their own:\n%s", out)
	}
}

func TestRenderQueueAt_PrettyNamesTheYearOfADistantFinish(t *testing.T) {
	qs := etaFixture()
	qs.Holders[0].ExpectedDurationMS = 200 * 24 * 60 * 60 * 1000
	var buf bytes.Buffer
	if err := opsview.RenderQueueAt(&buf, qs, "pretty", etaFixtureNow()); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	if line := lineWith(t, buf.String(), "hold-profiled"); !strings.Contains(line, "2027") {
		t.Fatalf("a finish in another year must name it: %q", line)
	}
}

func TestRenderQueueAt_PrettyDatesAFinishPastMidnight(t *testing.T) {
	qs := etaFixture()
	qs.Holders[0].ExpectedDurationMS = 20 * 60 * 60 * 1000
	var buf bytes.Buffer
	now := time.Date(2026, 9, 10, 23, 0, 0, 0, time.Local)
	if err := opsview.RenderQueueAt(&buf, qs, "pretty", now); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
	line := lineWith(t, buf.String(), "hold-profiled")
	if !strings.Contains(line, "Sep 11 18:59") {
		t.Fatalf("a finish on another day must carry the date: %q", line)
	}
	if strings.Contains(line, "2026") {
		t.Fatalf("a finish inside this year must not carry it: %q", line)
	}
}

func TestRenderQueueAt_JSONCarriesMillisecondsAndRFC3339(t *testing.T) {
	var buf bytes.Buffer
	now := etaFixtureNow()
	if err := opsview.RenderQueueAt(&buf, etaFixture(), "json", now); err != nil {
		t.Fatalf("render json: %v", err)
	}
	var got struct {
		UnmeasuredWaiters int `json:"unmeasured_waiters"`
		Holders           []struct {
			RunID               string `json:"run_id"`
			ExpectedRemainingMS *int64 `json:"expected_remaining_ms"`
			ExpectedFinishMS    *int64 `json:"expected_finish_ms"`
			ExpectedFinishAt    string `json:"expected_finish_at"`
		} `json:"holders"`
		Waiters []struct {
			RunID            string `json:"run_id"`
			ExpectedStartMS  *int64 `json:"expected_start_ms"`
			ExpectedStartAt  string `json:"expected_start_at"`
			ExpectedFinishMS *int64 `json:"expected_finish_ms"`
			ExpectedFinishAt string `json:"expected_finish_at"`
		} `json:"waiters"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode queue json: %v\n%s", err, buf.String())
	}
	if got.UnmeasuredWaiters != 1 {
		t.Fatalf("unmeasured_waiters = %d, want 1", got.UnmeasuredWaiters)
	}
	if len(got.Holders) != 2 || len(got.Waiters) != 2 {
		t.Fatalf("json dropped rows: %d holders, %d waiters", len(got.Holders), len(got.Waiters))
	}
	h := got.Holders[0]
	if h.ExpectedRemainingMS == nil || *h.ExpectedRemainingMS != 240_000 {
		t.Fatalf("holder expected_remaining_ms = %v, want 240000", h.ExpectedRemainingMS)
	}
	if h.ExpectedFinishMS == nil || *h.ExpectedFinishMS != 240_000 {
		t.Fatalf("holder expected_finish_ms = %v, want 240000", h.ExpectedFinishMS)
	}
	if want := now.Add(240 * time.Second).Format(time.RFC3339); h.ExpectedFinishAt != want {
		t.Fatalf("holder expected_finish_at = %q, want %q", h.ExpectedFinishAt, want)
	}
	if got.Holders[1].ExpectedRemainingMS != nil || got.Holders[1].ExpectedFinishAt != "" {
		t.Fatalf("unmeasured holder carries a fabricated estimate: %+v", got.Holders[1])
	}
	w := got.Waiters[0]
	if w.ExpectedFinishMS == nil || *w.ExpectedFinishMS != 210_000 {
		t.Fatalf("waiter expected_finish_ms = %v, want 210000", w.ExpectedFinishMS)
	}
	if want := now.Add(90 * time.Second).Format(time.RFC3339); w.ExpectedStartAt != want {
		t.Fatalf("waiter expected_start_at = %q, want %q", w.ExpectedStartAt, want)
	}
	if want := now.Add(210 * time.Second).Format(time.RFC3339); w.ExpectedFinishAt != want {
		t.Fatalf("waiter expected_finish_at = %q, want %q", w.ExpectedFinishAt, want)
	}
	if got.Waiters[1].ExpectedStartMS != nil || got.Waiters[1].ExpectedFinishMS != nil {
		t.Fatalf("unmeasured waiter carries a fabricated estimate: %+v", got.Waiters[1])
	}
}

func TestRenderQueueAt_PlainKeepsOneTabSeparatedRecordPerRow(t *testing.T) {
	var buf bytes.Buffer
	if err := opsview.RenderQueueAt(&buf, etaFixture(), "plain", etaFixtureNow()); err != nil {
		t.Fatalf("render plain: %v", err)
	}
	out := buf.String()
	holder := lineWith(t, out, "hold-profiled")
	if strings.Count(holder, "\t") < 13 {
		t.Fatalf("holder record lost its columns: %q", holder)
	}
	finish := etaFixtureNow().Add(240 * time.Second).Format(time.RFC3339)
	if !strings.HasSuffix(holder, "\t4m0s\t"+finish) {
		t.Fatalf("holder record must end with remaining and an RFC3339 finish: %q", holder)
	}
	waiter := lineWith(t, out, "queue-profiled")
	if !strings.HasSuffix(waiter, "\t"+etaFixtureNow().Add(210*time.Second).Format(time.RFC3339)) {
		t.Fatalf("waiter record must end with an RFC3339 expected finish: %q", waiter)
	}
	if !strings.Contains(out, "unmeasured-waiters\t1") {
		t.Fatalf("plain output omits the unmeasured waiter count:\n%s", out)
	}
}

func lineWith(t *testing.T, out, needle string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no line contains %q:\n%s", needle, out)
	return ""
}
