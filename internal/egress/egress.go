// Package egress bounds the bytes a Sparkwing service sends to clients.
//
// One Meter per process counts the response bodies of the download
// surfaces: artifact and cache-archive reads, log reads, the live log
// stream, and git proxy fetches. Every byte is counted twice, once
// against the principal that asked for it and once against the process
// total, so an operator can see both who spent the month's egress and
// how much left the deployment today.
//
// Two budgets act on those counters. A principal past its monthly byte
// budget is refused before the next download starts, which is the cap
// that stops one tenant turning a CI product into a file host. The
// global daily threshold refuses nothing; it raises an alarm the health
// route reports and the log carries at warn level, because the bill an
// operator needs to see early is the deployment's, not one principal's.
//
// Counting is in memory. [Meter.Dirty] hands a caller the principals
// whose totals have moved since the last call so a service with a store
// can persist them on a timer, and [Meter.Restore] loads them back at
// startup; nothing here writes one row per response.
package egress

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Class names the download surface a byte left through. It labels
// refusals and log lines; every class spends the same budget, because
// the bill an object store sends does not separate them.
type Class string

const (
	// ClassArtifact counts artifact and cache-archive downloads.
	ClassArtifact Class = "artifact"
	// ClassLog counts durable log reads.
	ClassLog Class = "log"
	// ClassLogStream counts the live log stream.
	ClassLogStream Class = "log_stream"
	// ClassGit counts git proxy and dependency proxy fetches.
	ClassGit Class = "git"
)

// AnonymousPrincipal labels bytes a service served without a bearer,
// which is what the dependency proxy answers. They share one budget, so
// an open route cannot be spent anonymously past the cap.
const AnonymousPrincipal = "anonymous"

// OverflowPrincipal labels bytes served to a principal the meter had no
// room to track separately. They share one budget rather than an
// unbudgeted one, so reaching the cardinality cap tightens the meter
// instead of opening it.
const OverflowPrincipal = "(overflow)"

// MaxPrincipals bounds how many principals one meter tracks. A
// controller mints tokens under an operator's hand, so the cap sits far
// above a real deployment's principal count and exists to bound memory
// against a fleet that invents identities.
const MaxPrincipals = 10000

// ErrBudgetExceeded matches every *BudgetError under errors.Is.
var ErrBudgetExceeded = errors.New("egress budget exceeded")

// BudgetError refuses a download that would spend a principal's monthly
// byte budget past its limit. The message names the limit, what is
// already spent, and when the budget resets, so a client that prints it
// needs nothing else to explain the refusal.
type BudgetError struct {
	Principal string
	Class     Class
	// LimitBytes is the monthly budget that refused this download.
	LimitBytes int64
	// UsedBytes is what the principal has already sent this month.
	UsedBytes int64
	// Month is the UTC month the budget covers, as "2006-01".
	Month string
	// RetryAfter is the wait until the budget resets.
	RetryAfter time.Duration
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf(
		"egress budget exceeded: %s has downloaded %s of its %s monthly budget, so this %s download is refused until the budget resets at the start of the next UTC month, in %s. "+
			"Raise it with --egress-monthly-bytes on the serving process, or wait out the reset",
		e.Principal, FormatBytes(e.UsedBytes), FormatBytes(e.LimitBytes), e.Class, roundWait(e.RetryAfter))
}

func (e *BudgetError) Unwrap() error { return ErrBudgetExceeded }

// ErrStreamLimit matches every *StreamLimitError under errors.Is.
var ErrStreamLimit = errors.New("live log stream limit reached")

// StreamLimitError refuses a live log stream because the principal
// already holds its cap of concurrent ones.
type StreamLimitError struct {
	Principal string
	Limit     int
	// RetryAfter is how long a client should wait before reopening.
	RetryAfter time.Duration
}

func (e *StreamLimitError) Error() string {
	return fmt.Sprintf(
		"live log stream limit reached: %s already holds %d concurrent live log streams, which is the cap, so this one is refused. "+
			"Close a stream and retry, or raise --egress-max-log-streams on the serving process",
		e.Principal, e.Limit)
}

func (e *StreamLimitError) Unwrap() error { return ErrStreamLimit }

// StreamRetryAfter is the wait a refused stream is told to observe. A
// stream ends when its node does, so a short retry is the honest answer.
const StreamRetryAfter = 30 * time.Second

// Config is a Meter's whole configuration. Every budget is off at zero,
// which is what a Sparkwing that was never given one serves.
type Config struct {
	// PerPrincipalMonthlyBytes refuses a principal's downloads once its
	// UTC-month total reaches this many bytes. Zero is unlimited.
	PerPrincipalMonthlyBytes int64
	// GlobalDailyAlarmBytes raises the alarm once the process has sent
	// this many bytes in a UTC day. It refuses nothing. Zero is off.
	GlobalDailyAlarmBytes int64
	// MaxStreamsPerPrincipal caps concurrent live log streams per
	// principal. Zero is unlimited.
	MaxStreamsPerPrincipal int
}

// Budgeted reports whether any budget in this configuration applies.
func (c Config) Budgeted() bool {
	return c.PerPrincipalMonthlyBytes > 0 || c.GlobalDailyAlarmBytes > 0 || c.MaxStreamsPerPrincipal > 0
}

// Usage is one principal's total for one UTC month, as a service
// persists and reloads it.
type Usage struct {
	Principal string
	// Month is the UTC month the bytes fell in, as "2006-01".
	Month string
	Bytes int64
}

// PrincipalState is one principal's counters at a point in time.
type PrincipalState struct {
	Principal  string `json:"principal"`
	MonthBytes int64  `json:"month_bytes"`
	DayBytes   int64  `json:"day_bytes"`
	Streams    int    `json:"streams"`
	OverBudget bool   `json:"over_budget"`
}

// State is the whole meter at a point in time, as the health route and
// the top-consumers view report it.
type State struct {
	MonthlyBytesPerPrincipal int64            `json:"monthly_bytes_per_principal"`
	DailyAlarmBytes          int64            `json:"daily_alarm_bytes"`
	MaxStreamsPerPrincipal   int              `json:"max_streams_per_principal"`
	Month                    string           `json:"month"`
	Day                      string           `json:"day"`
	GlobalMonthBytes         int64            `json:"global_month_bytes"`
	GlobalDayBytes           int64            `json:"global_day_bytes"`
	Alarm                    bool             `json:"alarm"`
	AlarmSince               time.Time        `json:"alarm_since,omitzero"`
	Refused                  uint64           `json:"refused_total"`
	Principals               int              `json:"principals"`
	Top                      []PrincipalState `json:"top,omitempty"`
}

// TopConsumers is how many principals [Meter.State] reports.
const TopConsumers = 10

type principalCounters struct {
	month      string
	monthBytes int64
	day        string
	dayBytes   int64
	streams    int
	dirty      bool
}

// Meter counts bytes sent per principal and for the process as a whole,
// and refuses the downloads that would spend a principal past its
// monthly budget. Safe for concurrent use.
type Meter struct {
	cfg    Config
	logger *slog.Logger

	mu         sync.Mutex
	now        func() time.Time
	principals map[string]*principalCounters
	month      string
	monthBytes int64
	day        string
	dayBytes   int64
	alarm      bool
	alarmSince time.Time
	refused    uint64
	overflowed bool
}

// New builds a Meter on cfg. A zero Config counts every byte and
// refuses nothing, which is what a deployment with no budget wants: the
// health route and the top-consumers view still answer.
func New(cfg Config) *Meter {
	return &Meter{
		cfg:        cfg,
		logger:     slog.Default(),
		now:        time.Now,
		principals: make(map[string]*principalCounters),
	}
}

// WithLogger routes the alarm line to l. The line is written at warn
// level so a deployment's alerting can key on it.
func (m *Meter) WithLogger(l *slog.Logger) *Meter {
	if l != nil {
		m.logger = l
	}
	return m
}

// Config returns the budgets this meter was built on.
func (m *Meter) Config() Config { return m.cfg }

// Check reports whether principal may start another download. It
// returns a *BudgetError when the monthly budget is spent and nil
// otherwise, including when no budget applies.
func (m *Meter) Check(principal string) error {
	if m == nil || m.cfg.PerPrincipalMonthlyBytes <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	st := m.counters(principal, now)
	if st.monthBytes < m.cfg.PerPrincipalMonthlyBytes {
		return nil
	}
	m.refused++
	return &BudgetError{
		Principal:  m.key(principal),
		LimitBytes: m.cfg.PerPrincipalMonthlyBytes,
		UsedBytes:  st.monthBytes,
		Month:      st.month,
		RetryAfter: untilNextMonth(now),
	}
}

// Record adds n bytes served to principal through class. It is called
// after the bytes reach the client, so a download already in flight
// finishes and its cost lands on the budget that refuses the next one.
func (m *Meter) Record(principal string, class Class, n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	now := m.now().UTC()
	st := m.counters(principal, now)
	st.monthBytes += n
	st.dayBytes += n
	st.dirty = true
	m.monthBytes += n
	m.dayBytes += n
	raised := m.raiseAlarmLocked(now)
	day, total := m.day, m.dayBytes
	m.mu.Unlock()

	if raised {
		m.logger.Warn("egress daily alarm",
			"day", day,
			"day_bytes", total,
			"threshold_bytes", m.cfg.GlobalDailyAlarmBytes,
			"principal", m.key(principal),
			"class", string(class))
	}
}

// Serve returns a ResponseWriter that records everything written
// through it against principal. It preserves the flush and deadline
// control a streaming handler reaches for through
// [http.NewResponseController].
func (m *Meter) Serve(w http.ResponseWriter, principal string, class Class) http.ResponseWriter {
	if m == nil {
		return w
	}
	counting := &countingWriter{ResponseWriter: w, meter: m, principal: principal, class: class}
	// safety: a stream handler asks whether its writer flushes before it
	// commits to SSE, so the wrapper answers that question the same way
	// the writer underneath it would.
	if _, ok := w.(http.Flusher); ok {
		return &flushingCountingWriter{countingWriter: counting}
	}
	return counting
}

// OpenStream reserves one of principal's concurrent live log streams
// and returns the release the caller defers. A principal already at the
// cap gets a *StreamLimitError and a release that does nothing.
func (m *Meter) OpenStream(principal string) (func(), error) {
	if m == nil || m.cfg.MaxStreamsPerPrincipal <= 0 {
		return func() {}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	st := m.counters(principal, now)
	if st.streams >= m.cfg.MaxStreamsPerPrincipal {
		m.refused++
		return func() {}, &StreamLimitError{
			Principal:  m.key(principal),
			Limit:      m.cfg.MaxStreamsPerPrincipal,
			RetryAfter: StreamRetryAfter,
		}
	}
	st.streams++
	key := m.key(principal)
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if held := m.principals[key]; held != nil && held.streams > 0 {
				held.streams--
			}
		})
	}, nil
}

// Dirty returns the usages whose byte totals have moved since the last
// call and clears the marks. A service with a store persists them on a
// timer; one without calls nothing and keeps counting in memory.
func (m *Meter) Dirty() []Usage {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Usage
	for name, st := range m.principals {
		if !st.dirty {
			continue
		}
		st.dirty = false
		out = append(out, Usage{Principal: name, Month: st.month, Bytes: st.monthBytes})
	}
	slices.SortFunc(out, func(a, b Usage) int {
		if a.Principal != b.Principal {
			if a.Principal < b.Principal {
				return -1
			}
			return 1
		}
		return 0
	})
	return out
}

// Restore loads persisted usages, so a restarted service resumes the
// month where it left off rather than handing every principal a fresh
// budget. A usage for another month is ignored, and a usage below what
// this process has already counted never lowers the total.
func (m *Meter) Restore(usages []Usage) {
	if m == nil || len(usages) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	month := now.Format("2006-01")
	for _, u := range usages {
		if u.Month != month || u.Bytes <= 0 || u.Principal == "" {
			continue
		}
		st := m.counters(u.Principal, now)
		if u.Bytes <= st.monthBytes {
			continue
		}
		m.monthBytes += u.Bytes - st.monthBytes
		st.monthBytes = u.Bytes
	}
}

// State snapshots the meter for the health route and the top-consumers
// view.
func (m *Meter) State() State {
	if m == nil {
		return State{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	m.rollGlobal(now)
	out := State{
		MonthlyBytesPerPrincipal: m.cfg.PerPrincipalMonthlyBytes,
		DailyAlarmBytes:          m.cfg.GlobalDailyAlarmBytes,
		MaxStreamsPerPrincipal:   m.cfg.MaxStreamsPerPrincipal,
		Month:                    m.month,
		Day:                      m.day,
		GlobalMonthBytes:         m.monthBytes,
		GlobalDayBytes:           m.dayBytes,
		Alarm:                    m.alarm,
		AlarmSince:               m.alarmSince,
		Refused:                  m.refused,
		Principals:               len(m.principals),
	}
	for name, st := range m.principals {
		m.rollPrincipal(st, now)
		if st.monthBytes == 0 && st.dayBytes == 0 && st.streams == 0 {
			continue
		}
		out.Top = append(out.Top, PrincipalState{
			Principal:  name,
			MonthBytes: st.monthBytes,
			DayBytes:   st.dayBytes,
			Streams:    st.streams,
			OverBudget: m.cfg.PerPrincipalMonthlyBytes > 0 && st.monthBytes >= m.cfg.PerPrincipalMonthlyBytes,
		})
	}
	slices.SortFunc(out.Top, func(a, b PrincipalState) int {
		switch {
		case a.MonthBytes != b.MonthBytes:
			return int(sign(b.MonthBytes - a.MonthBytes))
		case a.Principal < b.Principal:
			return -1
		case a.Principal > b.Principal:
			return 1
		default:
			return 0
		}
	})
	if len(out.Top) > TopConsumers {
		out.Top = out.Top[:TopConsumers]
	}
	return out
}

// Alarm reports whether the process is past its daily egress threshold.
func (m *Meter) Alarm() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollGlobal(m.now().UTC())
	return m.alarm
}

func sign(n int64) int64 {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
	}
}

// safety: a principal past the cardinality cap folds into one shared
// bucket, so the budget a caller spends is never wider than the one the
// meter can account for.
func (m *Meter) key(principal string) string {
	if principal == "" {
		return AnonymousPrincipal
	}
	if _, tracked := m.principals[principal]; !tracked && len(m.principals) >= MaxPrincipals {
		return OverflowPrincipal
	}
	return principal
}

func (m *Meter) counters(principal string, now time.Time) *principalCounters {
	m.rollGlobal(now)
	key := m.key(principal)
	if key == OverflowPrincipal && !m.overflowed {
		m.overflowed = true
		m.logger.Warn("egress principal cap reached",
			"principals", len(m.principals), "shared_budget", OverflowPrincipal)
	}
	st := m.principals[key]
	if st == nil {
		st = &principalCounters{month: m.month, day: m.day}
		m.principals[key] = st
	}
	m.rollPrincipal(st, now)
	return st
}

func (m *Meter) rollPrincipal(st *principalCounters, now time.Time) {
	month, day := now.Format("2006-01"), now.Format("2006-01-02")
	if st.month != month {
		st.month = month
		st.monthBytes = 0
		st.dirty = false
	}
	if st.day != day {
		st.day = day
		st.dayBytes = 0
	}
}

// safety: the day total drives the alarm, so rolling it also clears the
// alarm; an operator paged for yesterday's bytes must not stay paged.
func (m *Meter) rollGlobal(now time.Time) {
	month, day := now.Format("2006-01"), now.Format("2006-01-02")
	if m.month != month {
		m.month = month
		m.monthBytes = 0
	}
	if m.day != day {
		m.day = day
		m.dayBytes = 0
		m.alarm = false
		m.alarmSince = time.Time{}
	}
}

func (m *Meter) raiseAlarmLocked(now time.Time) bool {
	if m.cfg.GlobalDailyAlarmBytes <= 0 || m.alarm || m.dayBytes < m.cfg.GlobalDailyAlarmBytes {
		return false
	}
	m.alarm = true
	m.alarmSince = now
	return true
}

func untilNextMonth(now time.Time) time.Duration {
	next := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	if wait := next.Sub(now); wait > 0 {
		return wait
	}
	return time.Second
}

func roundWait(d time.Duration) time.Duration {
	if d >= time.Hour {
		return d.Round(time.Hour)
	}
	return d.Round(time.Second)
}

// FormatBytes renders a byte count the way an operator reads a bill:
// whole units, two significant places past a kilobyte.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

type countingWriter struct {
	http.ResponseWriter
	meter     *Meter
	principal string
	class     Class
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.meter.Record(w.principal, w.class, int64(n))
	return n, err
}

// Unwrap hands the underlying writer to [http.NewResponseController],
// so a streaming handler keeps its flush and its write deadline.
func (w *countingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type flushingCountingWriter struct {
	*countingWriter
}

func (w *flushingCountingWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }
