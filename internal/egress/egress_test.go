package egress_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
)

func at(t *testing.T, stamp string) time.Time {
	t.Helper()
	when, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("parse %q: %v", stamp, err)
	}
	return when.UTC()
}

func meterAt(t *testing.T, cfg egress.Config, stamp string) (*egress.Meter, *time.Time) {
	t.Helper()
	clock := at(t, stamp)
	m := egress.New(cfg)
	egress.SetClock(m, func() time.Time { return clock })
	return m, &clock
}

func TestUnbudgetedMeterCountsAndRefusesNothing(t *testing.T) {
	m, _ := meterAt(t, egress.Config{}, "2026-09-13T10:00:00Z")
	m.Record("alice", egress.ClassArtifact, 5000)
	if err := m.Check("alice"); err != nil {
		t.Fatalf("Check with no budget = %v, want nil", err)
	}
	state := m.State()
	if state.GlobalMonthBytes != 5000 || state.GlobalDayBytes != 5000 {
		t.Fatalf("global totals = month %d day %d, want 5000 and 5000", state.GlobalMonthBytes, state.GlobalDayBytes)
	}
	if len(state.Top) != 1 || state.Top[0].Principal != "alice" || state.Top[0].MonthBytes != 5000 {
		t.Fatalf("top consumers = %+v, want alice at 5000", state.Top)
	}
	if state.Alarm {
		t.Error("an unbudgeted meter raised the daily alarm")
	}
}

func TestMonthlyBudgetRefusesOnlyThePrincipalPastIt(t *testing.T) {
	m, _ := meterAt(t, egress.Config{PerPrincipalMonthlyBytes: 1000}, "2026-09-13T10:00:00Z")
	m.Record("alice", egress.ClassArtifact, 999)
	if err := m.Check("alice"); err != nil {
		t.Fatalf("Check one byte short of the budget = %v, want nil", err)
	}
	m.Record("alice", egress.ClassArtifact, 1)

	var budget *egress.BudgetError
	err := m.Check("alice")
	if !errors.As(err, &budget) {
		t.Fatalf("Check at the budget = %v, want a *BudgetError", err)
	}
	if !errors.Is(err, egress.ErrBudgetExceeded) {
		t.Error("a *BudgetError does not match ErrBudgetExceeded")
	}
	if budget.UsedBytes != 1000 || budget.LimitBytes != 1000 || budget.Principal != "alice" {
		t.Fatalf("refusal = %+v, want alice at 1000 of 1000", budget)
	}
	if budget.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %s, want the wait until the month rolls", budget.RetryAfter)
	}
	for _, want := range []string{"alice", "egress budget exceeded", "--egress-monthly-bytes"} {
		if !strings.Contains(budget.Error(), want) {
			t.Errorf("refusal message %q does not name %q", budget.Error(), want)
		}
	}
	if err := m.Check("bob"); err != nil {
		t.Fatalf("Check for a principal with its own budget = %v, want nil", err)
	}
}

func TestBudgetResetsWhenTheMonthRolls(t *testing.T) {
	m, clock := meterAt(t, egress.Config{PerPrincipalMonthlyBytes: 100}, "2026-09-30T23:59:00Z")
	m.Record("alice", egress.ClassLog, 100)
	if err := m.Check("alice"); err == nil {
		t.Fatal("Check at the budget = nil, want a refusal")
	}
	*clock = at(t, "2026-10-01T00:00:00Z")
	if err := m.Check("alice"); err != nil {
		t.Fatalf("Check after the month rolled = %v, want nil", err)
	}
	if state := m.State(); state.GlobalMonthBytes != 0 || state.Month != "2026-10" {
		t.Fatalf("state after the roll = %+v, want an empty October", state)
	}
}

func TestDailyAlarmRaisesOnceAndClearsWithTheDay(t *testing.T) {
	m, clock := meterAt(t, egress.Config{GlobalDailyAlarmBytes: 1000}, "2026-09-13T10:00:00Z")
	m.Record("alice", egress.ClassArtifact, 600)
	if m.Alarm() {
		t.Fatal("the alarm rose below the threshold")
	}
	m.Record("bob", egress.ClassArtifact, 400)
	if !m.Alarm() {
		t.Fatal("the alarm did not rise when the process reached the daily threshold")
	}
	state := m.State()
	if !state.Alarm || state.AlarmSince.IsZero() {
		t.Fatalf("state = %+v, want an alarm with a raise time", state)
	}
	if state.GlobalDayBytes != 1000 {
		t.Fatalf("GlobalDayBytes = %d, want 1000", state.GlobalDayBytes)
	}

	*clock = at(t, "2026-09-14T00:00:01Z")
	if m.Alarm() {
		t.Fatal("the alarm survived the day roll")
	}
	if state := m.State(); state.GlobalDayBytes != 0 || state.GlobalMonthBytes != 1000 {
		t.Fatalf("state after the day roll = %+v, want the day cleared and the month kept", state)
	}
}

func TestStreamCapRefusesPastTheLimitAndReleaseFreesASlot(t *testing.T) {
	m, _ := meterAt(t, egress.Config{MaxStreamsPerPrincipal: 2}, "2026-09-13T10:00:00Z")
	first, err := m.OpenStream("alice")
	if err != nil {
		t.Fatalf("first stream = %v, want nil", err)
	}
	second, err := m.OpenStream("alice")
	if err != nil {
		t.Fatalf("second stream = %v, want nil", err)
	}
	var limit *egress.StreamLimitError
	if err := func() error { _, err := m.OpenStream("alice"); return err }(); !errors.As(err, &limit) {
		t.Fatalf("third stream = %v, want a *StreamLimitError", err)
	}
	if !errors.Is(limit, egress.ErrStreamLimit) || limit.Limit != 2 {
		t.Fatalf("refusal = %+v, want the cap of 2", limit)
	}
	if _, err := m.OpenStream("bob"); err != nil {
		t.Fatalf("another principal's first stream = %v, want nil", err)
	}
	second()
	second()
	third, err := m.OpenStream("alice")
	if err != nil {
		t.Fatalf("stream after a release = %v, want nil", err)
	}
	first()
	third()
	if state := m.State(); len(state.Top) != 1 || state.Top[0].Principal != "bob" {
		t.Fatalf("state after every alice stream closed = %+v, want only bob holding one", state.Top)
	}
}

func TestServeCountsTheBytesTheHandlerWrites(t *testing.T) {
	m, _ := meterAt(t, egress.Config{PerPrincipalMonthlyBytes: 32}, "2026-09-13T10:00:00Z")
	rec := httptest.NewRecorder()
	out := m.Serve(rec, "alice", egress.ClassArtifact)
	if _, err := out.Write([]byte("0123456789")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := out.Write([]byte("0123456789")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := m.Check("alice"); err != nil {
		t.Fatalf("Check under the budget = %v, want nil", err)
	}
	if _, err := out.Write([]byte("0123456789ab")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := m.Check("alice"); err == nil {
		t.Fatal("Check after 32 bytes = nil, want a refusal")
	}
	if body := rec.Body.String(); body != "01234567890123456789"+"0123456789ab" {
		t.Fatalf("body = %q, the counting writer changed it", body)
	}
}

func TestServeKeepsTheStreamControlsAHandlerAsksFor(t *testing.T) {
	m := egress.New(egress.Config{})
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(done)
		out := m.Serve(w, "alice", egress.ClassLogStream)
		f, ok := out.(http.Flusher)
		if !ok {
			t.Error("the counting writer dropped http.Flusher")
			return
		}
		if err := http.NewResponseController(out).SetWriteDeadline(time.Time{}); err != nil {
			t.Errorf("SetWriteDeadline through the counting writer: %v", err)
			return
		}
		if _, err := out.Write([]byte("data: hello\n\n")); err != nil {
			t.Error(err)
			return
		}
		f.Flush()
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	<-done
	if state := m.State(); state.GlobalDayBytes != int64(len("data: hello\n\n")) {
		t.Fatalf("GlobalDayBytes = %d, want the streamed body's %d", state.GlobalDayBytes, len("data: hello\n\n"))
	}
}

func TestDirtyReportsMovedTotalsOnceAndRestoreResumesThem(t *testing.T) {
	m, _ := meterAt(t, egress.Config{PerPrincipalMonthlyBytes: 1000}, "2026-09-13T10:00:00Z")
	m.Record("alice", egress.ClassArtifact, 400)
	m.Record("bob", egress.ClassLog, 100)
	dirty := m.Dirty()
	if len(dirty) != 2 || dirty[0].Principal != "alice" || dirty[0].Bytes != 400 || dirty[0].Month != "2026-09" {
		t.Fatalf("Dirty = %+v, want alice at 400 and bob at 100 for 2026-09", dirty)
	}
	if again := m.Dirty(); len(again) != 0 {
		t.Fatalf("Dirty with nothing moved = %+v, want empty", again)
	}
	m.Record("alice", egress.ClassArtifact, 1)
	if moved := m.Dirty(); len(moved) != 1 || moved[0].Principal != "alice" || moved[0].Bytes != 401 {
		t.Fatalf("Dirty after one more byte = %+v, want alice at 401", moved)
	}

	restarted, _ := meterAt(t, egress.Config{PerPrincipalMonthlyBytes: 1000}, "2026-09-13T11:00:00Z")
	restarted.Restore([]egress.Usage{
		{Principal: "alice", Month: "2026-09", Bytes: 401},
		{Principal: "carol", Month: "2026-08", Bytes: 900},
	})
	state := restarted.State()
	if state.GlobalMonthBytes != 401 {
		t.Fatalf("GlobalMonthBytes after Restore = %d, want 401; a stale month must not load", state.GlobalMonthBytes)
	}
	if len(state.Top) != 1 || state.Top[0].Principal != "alice" || state.Top[0].MonthBytes != 401 {
		t.Fatalf("top consumers after Restore = %+v, want alice at 401", state.Top)
	}
}

func TestRestoreNeverLowersWhatThisProcessCounted(t *testing.T) {
	m, _ := meterAt(t, egress.Config{PerPrincipalMonthlyBytes: 1000}, "2026-09-13T10:00:00Z")
	m.Record("alice", egress.ClassArtifact, 900)
	m.Restore([]egress.Usage{{Principal: "alice", Month: "2026-09", Bytes: 100}})
	if err := m.Check("alice"); err != nil {
		t.Fatalf("Check after a lower persisted total = %v, want the in-memory 900 to stand", err)
	}
	m.Record("alice", egress.ClassArtifact, 100)
	if err := m.Check("alice"); err == nil {
		t.Fatal("Check at 1000 = nil, want a refusal")
	}
}

func TestAnonymousBytesShareOneBudget(t *testing.T) {
	m, _ := meterAt(t, egress.Config{PerPrincipalMonthlyBytes: 100}, "2026-09-13T10:00:00Z")
	m.Record("", egress.ClassGit, 60)
	m.Record("", egress.ClassGit, 40)
	var budget *egress.BudgetError
	if err := m.Check(""); !errors.As(err, &budget) {
		t.Fatalf("Check for anonymous = %v, want a refusal", err)
	}
	if budget.Principal != egress.AnonymousPrincipal {
		t.Fatalf("refused principal = %q, want %q", budget.Principal, egress.AnonymousPrincipal)
	}
}

func TestConcurrentRecordsSumExactly(t *testing.T) {
	m, _ := meterAt(t, egress.Config{}, "2026-09-13T10:00:00Z")
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				m.Record("alice", egress.ClassArtifact, 7)
			}
		}()
	}
	wg.Wait()
	if state := m.State(); state.GlobalMonthBytes != 50*20*7 {
		t.Fatalf("GlobalMonthBytes = %d, want %d", state.GlobalMonthBytes, 50*20*7)
	}
}

func TestFormatBytesReadsAsABill(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{1024, "1.00 KiB"},
		{1536, "1.50 KiB"},
		{1 << 30, "1.00 GiB"},
		{1 << 40, "1.00 TiB"},
	} {
		if got := egress.FormatBytes(tc.in); got != tc.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBudgetedReportsWhetherAnyBudgetApplies(t *testing.T) {
	if (egress.Config{}).Budgeted() {
		t.Error("a zero Config reports a budget")
	}
	for _, cfg := range []egress.Config{
		{PerPrincipalMonthlyBytes: 1},
		{GlobalDailyAlarmBytes: 1},
		{MaxStreamsPerPrincipal: 1},
	} {
		if !cfg.Budgeted() {
			t.Errorf("%+v reports no budget", cfg)
		}
	}
}
