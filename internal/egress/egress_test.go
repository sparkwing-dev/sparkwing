package egress

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
)

func at(t *testing.T, stamp string) time.Time {
	t.Helper()
	when, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("parse %q: %v", stamp, err)
	}
	return when.UTC()
}

func meterAt(t *testing.T, cfg Config, stamp string) (*Meter, *time.Time) {
	t.Helper()
	clock := at(t, stamp)
	m := New(cfg)
	setClock(m, func() time.Time { return clock })
	return m, &clock
}

func monthBytes(m *Meter, principal string) int64 {
	for _, p := range m.State().Top {
		if p.Principal == principal {
			return p.MonthBytes
		}
	}
	return 0
}

func TestUnbudgetedMeterCountsAndRefusesNothing(t *testing.T) {
	m, _ := meterAt(t, Config{}, "2026-09-13T10:00:00Z")
	m.record("alice", ClassArtifact, 5000)
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

func TestDailyAlarmRaisesOnceAndClearsWithTheDay(t *testing.T) {
	m, clock := meterAt(t, Config{GlobalDailyAlarmBytes: 1000}, "2026-09-13T10:00:00Z")
	m.record("alice", ClassArtifact, 600)
	if m.alarmed() {
		t.Fatal("the alarm rose below the threshold")
	}
	m.record("bob", ClassArtifact, 400)
	if !m.alarmed() {
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
	if m.alarmed() {
		t.Fatal("the alarm survived the day roll")
	}
	if state := m.State(); state.GlobalDayBytes != 0 || state.GlobalMonthBytes != 1000 {
		t.Fatalf("state after the day roll = %+v, want the day cleared and the month kept", state)
	}
}

func TestSlotCapsRefusePastTheLimitAndReleaseFreesASlot(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		slot Slot
	}{
		{"streams", Config{MaxStreamsPerPrincipal: 2}, SlotLogStream},
		{"downloads", Config{MaxDownloadsPerPrincipal: 2}, SlotDownload},
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
			var limit *ConcurrencyError
			if err := func() error { _, err := m.Open("alice", tc.slot); return err }(); !errors.As(err, &limit) {
				t.Fatalf("third = %v, want a *ConcurrencyError", err)
			}
			if !errors.Is(limit, errConcurrencyLimit) || limit.Limit != 2 || limit.Slot != tc.slot {
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
	m, _ := meterAt(t, Config{MaxStreamsPerPrincipal: 1, MaxDownloadsPerPrincipal: 1}, "2026-09-13T10:00:00Z")
	stream, err := m.Open("alice", SlotLogStream)
	if err != nil {
		t.Fatal(err)
	}
	defer stream()
	download, err := m.Open("alice", SlotDownload)
	if err != nil {
		t.Fatalf("a download while a stream is open = %v, want nil", err)
	}
	defer download()
	if _, err := m.Open("alice", SlotDownload); err == nil {
		t.Fatal("a second download past the cap was admitted")
	}
}

func TestServeCountsTheBytesTheHandlerWrites(t *testing.T) {
	m, _ := meterAt(t, Config{}, "2026-09-13T10:00:00Z")
	rec := httptest.NewRecorder()
	out := m.serveResponse(rec, httptest.NewRequest(http.MethodGet, "/a", nil), "alice", ClassArtifact)
	for _, chunk := range []string{"0123456789", "0123456789", "0123456789ab"} {
		if _, err := out.Write([]byte(chunk)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got := monthBytes(m, "alice"); got != 32 {
		t.Fatalf("alice's bytes = %d, want the 32 written", got)
	}
	if body := rec.Body.String(); body != "01234567890123456789"+"0123456789ab" {
		t.Fatalf("body = %q, the counting writer changed it", body)
	}
}

func TestServeKeepsTheStreamControlsAHandlerAsksFor(t *testing.T) {
	m := New(Config{})
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		out := m.serveResponse(w, r, "alice", ClassLogStream)
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

func TestRestoreResumesTheCurrentMonth(t *testing.T) {
	restarted, _ := meterAt(t, Config{}, "2026-09-13T11:00:00Z")
	restarted.Restore([]Usage{
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
	m, _ := meterAt(t, Config{}, "2026-09-13T10:00:00Z")
	m.record("alice", ClassArtifact, 900)
	m.Restore([]Usage{{Principal: "alice", Month: "2026-09", Bytes: 100}})
	if got := monthBytes(m, "alice"); got != 900 {
		t.Fatalf("alice after a lower persisted total = %d, want the in-memory 900 to stand", got)
	}
}

func TestAnonymousBytesShareOneCount(t *testing.T) {
	m, _ := meterAt(t, Config{}, "2026-09-13T10:00:00Z")
	m.record("", ClassArtifact, 60)
	m.record("", ClassArtifact, 40)
	if got := monthBytes(m, anonymousPrincipal); got != 100 {
		t.Fatalf("anonymous bytes = %d, want 100 under %q", got, anonymousPrincipal)
	}
}

// safety: the daily alarm is set below what the goroutines will send, so
// the alarm branch of Record runs while other goroutines are writing the
// principal map; without the key captured under the lock this is a
// "concurrent map read and map write" under -race.
func TestConcurrentRecordsSumExactly(t *testing.T) {
	m, _ := meterAt(t, Config{GlobalDailyAlarmBytes: 1}, "2026-09-13T10:00:00Z")
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				m.record(fmt.Sprintf("principal-%d", i), ClassArtifact, 7)
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

func TestBudgetedReportsWhetherAnyBudgetApplies(t *testing.T) {
	if (Config{}).budgeted() {
		t.Error("a zero Config reports a budget")
	}
	for _, cfg := range []Config{
		{GlobalDailyAlarmBytes: 1},
		{MaxDownloadsPerPrincipal: 1},
		{MaxStreamsPerPrincipal: 1},
	} {
		if !cfg.budgeted() {
			t.Errorf("%+v reports no budget", cfg)
		}
	}
}

func TestHeadRequestsChargeNothing(t *testing.T) {
	m, _ := meterAt(t, Config{GlobalDailyAlarmBytes: 1}, "2026-09-13T10:00:00Z")
	rec := httptest.NewRecorder()
	out := m.serveResponse(rec, httptest.NewRequest(http.MethodHead, "/a", nil), "alice", ClassArtifact)
	// safety: net/http discards this body, so it must not move the daily alarm.
	if _, err := out.Write([]byte(strings.Repeat("x", 4096))); err != nil {
		t.Fatal(err)
	}
	if state := m.State(); state.GlobalDayBytes != 0 {
		t.Fatalf("a HEAD charged %d bytes, want 0", state.GlobalDayBytes)
	}
	if m.alarmed() {
		t.Error("a HEAD raised the daily alarm")
	}
	if !Bodyless(httptest.NewRequest(http.MethodHead, "/a", nil)) {
		t.Error("Bodyless does not recognize HEAD")
	}
	if Bodyless(httptest.NewRequest(http.MethodGet, "/a", nil)) {
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
			m, _ := meterAt(t, Config{}, "2026-09-13T10:00:00Z")
			rec := httptest.NewRecorder()
			out := m.serveResponse(rec, httptest.NewRequest(http.MethodGet, "/a", nil), "alice", ClassArtifact)
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
	m := New(Config{})
	hijacked := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := m.serveResponse(w, r, "alice", ClassArtifact)
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

func TestPrincipalsPastTheCapShareTheOverflowCount(t *testing.T) {
	m, clock := meterAt(t, Config{}, "2026-09-13T10:00:00Z")
	for i := range MaxPrincipals {
		m.record(fmt.Sprintf("p%d", i), ClassArtifact, 1)
	}
	if got := m.State().Principals; got != MaxPrincipals {
		t.Fatalf("tracked %d principals, want the cap of %d", got, MaxPrincipals)
	}

	m.record("late-1", ClassArtifact, 6)
	m.record("late-2", ClassArtifact, 6)
	if got := monthBytes(m, overflowPrincipal); got != 12 {
		t.Fatalf("%s = %d bytes, want both untracked principals' 12", overflowPrincipal, got)
	}
	if got := m.State().Principals; got != MaxPrincipals+1 {
		t.Fatalf("tracked %d principals, want the cap plus the overflow bucket", got)
	}

	// safety: a month roll drops the principals that spent nothing, so the
	// fold is not permanent.
	*clock = at(t, "2026-10-01T00:00:00Z")
	if got := m.State().Principals; got != 0 {
		t.Fatalf("tracked %d principals after the month rolled, want every idle one dropped", got)
	}
}

func TestConcurrentRecordsRunTheAlarmBranchUnderContention(t *testing.T) {
	m := New(Config{GlobalDailyAlarmBytes: 1})
	var ticks atomic.Int64
	base := at(t, "2026-09-13T10:00:00Z")
	setClock(m, func() time.Time {
		return base.Add(time.Duration(ticks.Add(1)) * 24 * time.Hour)
	})

	var wg sync.WaitGroup
	for g := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				m.record(fmt.Sprintf("principal-%d-%d", g, i), ClassArtifact, 7)
			}
		}()
	}
	wg.Wait()
	if m.State().Refused != 0 {
		t.Error("the daily alarm refused a request")
	}
}

func TestStateRollsEachPrincipalsDayWithTheProcesss(t *testing.T) {
	m, clock := meterAt(t, Config{}, "2026-09-13T23:59:00Z")
	m.record("alice", ClassArtifact, 400)
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

// A restart later in the month resumes the month's total from the last saved
// day, while that day's own total no longer counts; another month's total is
// ignored.
func TestRestoreDayResumesTheMonthOnALaterDay(t *testing.T) {
	restarted, _ := meterAt(t, Config{}, "2026-09-20T08:00:00Z")
	restarted.RestoreDay(DayUsage{Day: "2026-08-31", Bytes: 9000, Month: "2026-08", MonthBytes: 9000})
	restarted.RestoreDay(DayUsage{Day: "2026-09-13", Bytes: 700, Month: "2026-09", MonthBytes: 700})
	state := restarted.State()
	if state.GlobalMonthBytes != 700 || state.GlobalDayBytes != 0 {
		t.Fatalf("after a restart a week later = month %d, day %d; want the month's 700 and a fresh day",
			state.GlobalMonthBytes, state.GlobalDayBytes)
	}
	restarted.record("bob", ClassArtifact, 50)
	if state := restarted.State(); state.GlobalMonthBytes != 750 || state.GlobalDayBytes != 50 {
		t.Fatalf("after one more charge = month %d, day %d; want the month carried forward to 750 and the day at 50",
			state.GlobalMonthBytes, state.GlobalDayBytes)
	}
}

func TestRestoreDayResumesTodaysTotal(t *testing.T) {
	restarted, _ := meterAt(t, Config{}, "2026-09-13T11:00:00Z")
	restarted.RestoreDay(DayUsage{Day: "2026-09-12", Bytes: 5000})
	if state := restarted.State(); state.GlobalDayBytes != 0 {
		t.Fatalf("GlobalDayBytes after restoring yesterday = %d, want 0", state.GlobalDayBytes)
	}
	restarted.RestoreDay(DayUsage{Day: "2026-09-13", Bytes: 1000, Month: "2026-09", MonthBytes: 1000})
	restarted.RestoreDay(DayUsage{Day: "2026-09-13", Bytes: 10})
	if state := restarted.State(); state.GlobalDayBytes != 1000 {
		t.Fatalf("GlobalDayBytes after RestoreDay = %d, want 1000 and never lowered", state.GlobalDayBytes)
	}
}

func TestOneTeamFillingTheCapNeverSharesABudgetWithAnotherTeam(t *testing.T) {
	m, _ := meterAt(t, Config{MaxDownloadsPerPrincipal: 1}, "2026-09-13T10:00:00Z")
	for i := range MaxPrincipals {
		m.record(TeamPrincipal("a", fmt.Sprintf("actions:%d", i)), ClassArtifact, 1)
	}
	releaseB, err := m.Open(TeamPrincipal("b", "runner"), SlotDownload)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()
	releaseC, err := m.Open(TeamPrincipal("c", "runner"), SlotDownload)
	if err != nil {
		t.Fatalf("team c's first download shared team b's overflow slot: %v", err)
	}
	defer releaseC()
	releaseA, err := m.Open(TeamPrincipal("a", "late"), SlotDownload)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	var refused *ConcurrencyError
	if _, err := m.Open(TeamPrincipal("a", "later"), SlotDownload); !errors.As(err, &refused) || refused.Principal != TeamPrincipal("a", overflowPrincipal) {
		t.Fatalf("team a's untracked principals did not share team a's overflow: %v", err)
	}
}
