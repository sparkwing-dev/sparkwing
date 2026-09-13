package egress_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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
	for _, want := range []string{"alice", "egress budget exceeded", egress.FlagMonthlyBytes} {
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

func TestSlotCapsRefusePastTheLimitAndReleaseFreesASlot(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  egress.Config
		slot egress.Slot
	}{
		{"streams", egress.Config{MaxStreamsPerPrincipal: 2}, egress.SlotLogStream},
		{"downloads", egress.Config{MaxDownloadsPerPrincipal: 2}, egress.SlotDownload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := meterAt(t, tc.cfg, "2026-09-13T10:00:00Z")
			first, err := m.Open("alice", tc.slot)
			if err != nil {
				t.Fatalf("first = %v, want nil", err)
			}
			second, err := m.Open("alice", tc.slot)
			if err != nil {
				t.Fatalf("second = %v, want nil", err)
			}
			var limit *egress.ConcurrencyError
			if err := func() error { _, err := m.Open("alice", tc.slot); return err }(); !errors.As(err, &limit) {
				t.Fatalf("third = %v, want a *ConcurrencyError", err)
			}
			if !errors.Is(limit, egress.ErrConcurrencyLimit) || limit.Limit != 2 || limit.Slot != tc.slot {
				t.Fatalf("refusal = %+v, want the cap of 2 on %s", limit, tc.slot)
			}
			if _, err := m.Open("bob", tc.slot); err != nil {
				t.Fatalf("another principal's first = %v, want nil", err)
			}
			second()
			second()
			third, err := m.Open("alice", tc.slot)
			if err != nil {
				t.Fatalf("after a release = %v, want nil", err)
			}
			first()
			third()
			if state := m.State(); len(state.Top) != 1 || state.Top[0].Principal != "bob" {
				t.Fatalf("state after every alice slot closed = %+v, want only bob holding one", state.Top)
			}
		})
	}
}

// safety: the two slots are separate budgets, so a principal holding its
// cap of streams may still start a download.
func TestSlotsAreCountedApart(t *testing.T) {
	m, _ := meterAt(t, egress.Config{MaxStreamsPerPrincipal: 1, MaxDownloadsPerPrincipal: 1}, "2026-09-13T10:00:00Z")
	stream, err := m.Open("alice", egress.SlotLogStream)
	if err != nil {
		t.Fatal(err)
	}
	defer stream()
	download, err := m.Open("alice", egress.SlotDownload)
	if err != nil {
		t.Fatalf("a download while a stream is open = %v, want nil", err)
	}
	defer download()
	if _, err := m.Open("alice", egress.SlotDownload); err == nil {
		t.Fatal("a second download past the cap was admitted")
	}
}

func TestServeCountsTheBytesTheHandlerWrites(t *testing.T) {
	m, _ := meterAt(t, egress.Config{PerPrincipalMonthlyBytes: 32}, "2026-09-13T10:00:00Z")
	rec := httptest.NewRecorder()
	out := m.Serve(rec, httptest.NewRequest(http.MethodGet, "/a", nil), "alice", egress.ClassArtifact)
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		out := m.Serve(w, r, "alice", egress.ClassLogStream)
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

// safety: the daily alarm is set below what the goroutines will send, so
// the alarm branch of Record runs while other goroutines are writing the
// principal map; without the key captured under the lock this is a
// "concurrent map read and map write" under -race.
func TestConcurrentRecordsSumExactly(t *testing.T) {
	m, _ := meterAt(t, egress.Config{GlobalDailyAlarmBytes: 1}, "2026-09-13T10:00:00Z")
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				m.Record(fmt.Sprintf("principal-%d", i), egress.ClassArtifact, 7)
			}
		}()
	}
	wg.Wait()
	state := m.State()
	if state.GlobalMonthBytes != 50*20*7 {
		t.Fatalf("GlobalMonthBytes = %d, want %d", state.GlobalMonthBytes, 50*20*7)
	}
	if !state.Alarm {
		t.Error("the alarm did not rise, so the racing branch never ran")
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

func TestHeadRequestsChargeNothing(t *testing.T) {
	m, _ := meterAt(t, egress.Config{GlobalDailyAlarmBytes: 1}, "2026-09-13T10:00:00Z")
	rec := httptest.NewRecorder()
	out := m.Serve(rec, httptest.NewRequest(http.MethodHead, "/a", nil), "alice", egress.ClassArtifact)
	// safety: net/http discards this, so charging it let a HEAD loop latch
	// the daily alarm with nothing on the wire.
	if _, err := out.Write([]byte(strings.Repeat("x", 4096))); err != nil {
		t.Fatal(err)
	}
	if state := m.State(); state.GlobalDayBytes != 0 {
		t.Fatalf("a HEAD charged %d bytes, want 0", state.GlobalDayBytes)
	}
	if m.Alarm() {
		t.Error("a HEAD raised the daily alarm")
	}
	if !egress.Bodyless(httptest.NewRequest(http.MethodHead, "/a", nil)) {
		t.Error("Bodyless does not recognize HEAD")
	}
	if egress.Bodyless(httptest.NewRequest(http.MethodGet, "/a", nil)) {
		t.Error("Bodyless treats GET as bodyless")
	}
}

func TestOnlySuccessfulResponsesAreCharged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   int64
	}{
		{"ok", http.StatusOK, 5},
		{"partial", http.StatusPartialContent, 5},
		{"not found", http.StatusNotFound, 0},
		{"server error", http.StatusInternalServerError, 0},
		{"redirect", http.StatusFound, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := meterAt(t, egress.Config{}, "2026-09-13T10:00:00Z")
			rec := httptest.NewRecorder()
			out := m.Serve(rec, httptest.NewRequest(http.MethodGet, "/a", nil), "alice", egress.ClassArtifact)
			out.WriteHeader(tc.status)
			if _, err := out.Write([]byte("12345")); err != nil {
				t.Fatal(err)
			}
			if got := m.State().GlobalDayBytes; got != tc.want {
				t.Fatalf("a %d response charged %d bytes, want %d", tc.status, got, tc.want)
			}
		})
	}
}

func TestServeForwardsHijack(t *testing.T) {
	m := egress.New(egress.Config{})
	hijacked := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := m.Serve(w, r, "alice", egress.ClassGit)
		h, ok := out.(http.Hijacker)
		if !ok {
			hijacked <- errors.New("the counting writer dropped http.Hijacker")
			return
		}
		conn, buf, err := h.Hijack()
		if err != nil {
			hijacked <- err
			return
		}
		defer func() { _ = conn.Close() }()
		if _, err := buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi"); err != nil {
			hijacked <- err
			return
		}
		hijacked <- buf.Flush()
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := <-hijacked; err != nil {
		t.Fatal(err)
	}
}

func TestPrincipalsPastTheCapShareTheOverflowBudget(t *testing.T) {
	m, clock := meterAt(t, egress.Config{PerPrincipalMonthlyBytes: 10}, "2026-09-13T10:00:00Z")
	for i := range egress.MaxPrincipals {
		m.Record(fmt.Sprintf("p%d", i), egress.ClassArtifact, 1)
	}
	if got := m.State().Principals; got != egress.MaxPrincipals {
		t.Fatalf("tracked %d principals, want the cap of %d", got, egress.MaxPrincipals)
	}

	m.Record("late-1", egress.ClassArtifact, 6)
	m.Record("late-2", egress.ClassArtifact, 6)
	var budget *egress.BudgetError
	// safety: the fold must tighten the meter, not open it, so two
	// untracked principals spend one shared budget rather than none.
	if err := m.Check("late-3"); !errors.As(err, &budget) {
		t.Fatalf("Check for an untracked principal = %v, want a refusal", err)
	}
	if budget.Principal != egress.OverflowPrincipal || budget.UsedBytes != 12 {
		t.Fatalf("refusal = %+v, want %s at 12 bytes", budget, egress.OverflowPrincipal)
	}
	if got := m.State().Principals; got != egress.MaxPrincipals+1 {
		t.Fatalf("tracked %d principals, want the cap plus the overflow bucket", got)
	}

	// safety: a month roll drops the principals that spent nothing, so the
	// fold is not permanent.
	*clock = at(t, "2026-10-01T00:00:00Z")
	if got := m.State().Principals; got != 0 {
		t.Fatalf("tracked %d principals after the month rolled, want every idle one dropped", got)
	}
	if err := m.Check("late-3"); err != nil {
		t.Fatalf("Check after the roll = %v, want nil", err)
	}
}

func TestAMonthsClosingBytesSurviveTheRoll(t *testing.T) {
	m, clock := meterAt(t, egress.Config{}, "2026-09-30T23:59:59Z")
	m.WithPersistence()
	m.Record("alice", egress.ClassArtifact, 400)

	// safety: the flush that would have persisted September lands after
	// the roll, so the closing total must still be offered under September.
	*clock = at(t, "2026-10-01T00:00:01Z")
	m.Record("alice", egress.ClassArtifact, 5)

	dirty := m.Dirty()
	if len(dirty) != 2 {
		t.Fatalf("Dirty = %+v, want the closing September row and the October one", dirty)
	}
	if dirty[0].Month != "2026-09" || dirty[0].Bytes != 400 {
		t.Fatalf("first usage = %+v, want September at 400", dirty[0])
	}
	if dirty[1].Month != "2026-10" || dirty[1].Bytes != 5 {
		t.Fatalf("second usage = %+v, want October at 5", dirty[1])
	}
	if again := m.Dirty(); len(again) != 0 {
		t.Fatalf("Dirty again = %+v, want empty", again)
	}
}

// safety: the alarm clears when the day rolls, so a clock that advances a
// day per reading makes Record take its alarm branch on almost every
// call. That is what puts the branch's own map access next to every other
// goroutine's write; with the branch reading the principal map after the
// unlock this is a "concurrent map read and map write" under -race, and
// with one alarm per run it is a window the detector can miss.
func TestConcurrentRecordsRunTheAlarmBranchUnderContention(t *testing.T) {
	m := egress.New(egress.Config{GlobalDailyAlarmBytes: 1})
	var ticks atomic.Int64
	base := at(t, "2026-09-13T10:00:00Z")
	egress.SetClock(m, func() time.Time {
		return base.Add(time.Duration(ticks.Add(1)) * 24 * time.Hour)
	})

	var wg sync.WaitGroup
	for g := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				m.Record(fmt.Sprintf("principal-%d-%d", g, i), egress.ClassArtifact, 7)
			}
		}()
	}
	wg.Wait()
	if m.State().Refused != 0 {
		t.Error("the daily alarm refused a request")
	}
}

// safety: only the controller drains a meter, so a logs or cache meter
// that parked a closing total per principal per month would grow for the
// life of the process.
func TestAnUndrainedMeterParksNothingAcrossManyMonths(t *testing.T) {
	m, clock := meterAt(t, egress.Config{}, "2026-01-15T10:00:00Z")
	for month := 1; month <= 36; month++ {
		*clock = at(t, fmt.Sprintf("%04d-%02d-15T10:00:00Z", 2026+(month-1)/12, (month-1)%12+1))
		for p := range 20 {
			m.Record(fmt.Sprintf("p%d", p), egress.ClassLog, 100)
		}
	}
	state := m.State()
	if state.Pending != 0 {
		t.Fatalf("an undrained meter parked %d usages across 36 month rolls, want 0", state.Pending)
	}
	if got := m.Dirty(); len(got) != 20 {
		t.Fatalf("Dirty = %d usages, want only the live month's 20", len(got))
	}
}

func TestADrainedMeterParksClosingMonthsAndCapsTheBacklog(t *testing.T) {
	m, clock := meterAt(t, egress.Config{}, "2026-01-15T10:00:00Z")
	m.WithPersistence()

	*clock = at(t, "2026-01-15T10:00:00Z")
	m.Record("alice", egress.ClassLog, 100)
	*clock = at(t, "2026-02-15T10:00:00Z")
	m.Record("alice", egress.ClassLog, 5)
	if state := m.State(); state.Pending != 1 {
		t.Fatalf("Pending = %d, want January parked for the next drain", state.Pending)
	}
	if got := m.Dirty(); len(got) != 2 || got[0].Month != "2026-01" {
		t.Fatalf("Dirty = %+v, want January then February", got)
	}
	if state := m.State(); state.Pending != 0 {
		t.Fatalf("Pending after a drain = %d, want 0", state.Pending)
	}

	// safety: a drain that has stopped must cap rather than grow; the store
	// keeps a high-water mark, so a dropped entry costs history alone.
	for i := range egress.MaxPendingUsages + 100 {
		*clock = at(t, fmt.Sprintf("2026-%02d-15T10:00:00Z", i%12+1))
		m.Record(fmt.Sprintf("p%d", i), egress.ClassLog, 1)
	}
	state := m.State()
	if state.Pending > egress.MaxPendingUsages {
		t.Fatalf("Pending = %d, want at most the cap of %d", state.Pending, egress.MaxPendingUsages)
	}
	if state.PendingDropped == 0 {
		t.Fatal("the backlog filled without reporting a drop")
	}
}

func TestSlotIdentityPrefersThePodOverThePrincipal(t *testing.T) {
	principal := "pool"
	bare := httptest.NewRequest(http.MethodGet, "/a", nil)
	if got := egress.SlotIdentity(bare, principal, store.RunnerIdentityHeader); got != principal {
		t.Errorf("a request naming no pod = %q, want the principal", got)
	}

	withHolder := httptest.NewRequest(http.MethodGet, "/a", nil)
	withHolder.Header.Set("X-Sparkwing-Claim-Holder", "holder-7")
	if got := egress.SlotIdentity(withHolder, principal, "X-Sparkwing-Claim-Holder"); got != "pool/holder-7" {
		t.Errorf("a claim holder = %q, want pool/holder-7", got)
	}

	withRunner := httptest.NewRequest(http.MethodGet, "/a", nil)
	withRunner.Header.Set(store.RunnerIdentityHeader, "runner-3")
	withRunner.Header.Set("X-Sparkwing-Claim-Holder", "holder-7")
	// safety: the runner header wins, so a pod that names itself is counted
	// as itself even while it holds a claim.
	if got := egress.SlotIdentity(withRunner, principal, store.RunnerIdentityHeader, "X-Sparkwing-Claim-Holder"); got != "pool/runner-3" {
		t.Errorf("a runner header = %q, want pool/runner-3", got)
	}

	if got := egress.SlotIdentity(nil, principal, store.RunnerIdentityHeader); got != principal {
		t.Errorf("no request = %q, want the principal", got)
	}
}

// safety: twenty pods under one bearer must each get the pool's cap, not
// share one; a shared cap refused nineteen of them at a cap of eight.
func TestAPoolsPodsHoldTheirOwnSlots(t *testing.T) {
	m, _ := meterAt(t, egress.Config{MaxDownloadsPerPrincipal: 1}, "2026-09-13T10:00:00Z")
	var releases []func()
	for pod := range 20 {
		req := httptest.NewRequest(http.MethodGet, "/a", nil)
		req.Header.Set(store.RunnerIdentityHeader, fmt.Sprintf("pod-%d", pod))
		release, err := m.Open(egress.SlotIdentity(req, "pool", store.RunnerIdentityHeader), egress.SlotDownload)
		if err != nil {
			t.Fatalf("pod %d was refused its first download: %v", pod, err)
		}
		releases = append(releases, release)
	}
	// safety: the cap still applies within one pod.
	req := httptest.NewRequest(http.MethodGet, "/a", nil)
	req.Header.Set(store.RunnerIdentityHeader, "pod-0")
	if _, err := m.Open(egress.SlotIdentity(req, "pool", store.RunnerIdentityHeader), egress.SlotDownload); err == nil {
		t.Fatal("one pod's second simultaneous download was admitted past the cap")
	}
	for _, release := range releases {
		release()
	}
}

func TestStateRollsEachPrincipalsDayWithTheProcesss(t *testing.T) {
	m, clock := meterAt(t, egress.Config{}, "2026-09-13T23:59:00Z")
	m.Record("alice", egress.ClassArtifact, 400)
	if state := m.State(); state.GlobalDayBytes != 400 || state.Top[0].DayBytes != 400 {
		t.Fatalf("before the roll = %+v", state)
	}

	*clock = at(t, "2026-09-14T00:00:01Z")
	state := m.State()
	if state.GlobalDayBytes != 0 {
		t.Fatalf("GlobalDayBytes after the roll = %d, want 0", state.GlobalDayBytes)
	}
	// safety: the view puts the two side by side, so a principal still
	// showing yesterday's bytes against a zeroed global reads as a bug in
	// the meter.
	if len(state.Top) != 1 || state.Top[0].DayBytes != 0 {
		t.Fatalf("per-principal day after the roll = %+v, want 0 beside the global", state.Top)
	}
	if state.Top[0].MonthBytes != 400 {
		t.Fatalf("the month was rolled with the day: %+v", state.Top)
	}
}
