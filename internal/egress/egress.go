// Package egress bounds the bytes a Sparkwing service sends to clients.
//
// One Meter per process counts the response bodies of the download
// surfaces: artifact and cache-archive reads, log reads, the live log
// stream, and git proxy fetches. Every byte is counted twice, once
// against the principal that asked for it and once against the process
// total, so an operator can see both who spent the month's egress and
// how much left this process today.
//
// The meter refuses no byte. A principal at its concurrency cap is refused
// one more simultaneous download or live log stream. The global daily
// threshold raises an alarm the health route and the metrics report and the
// log carries at warn level, because the bill an operator needs to see early
// is the process's, not one principal's. What a team may download in a day
// is the controller's per-team cap, which a service charges before a
// response's first byte, so no download is cut off part way.
//
// Counting is in memory. [Meter.Flush] and [Meter.FlushDay] hand moved
// totals to a caller's persistence operation and acknowledge them only
// after that operation succeeds. [Meter.Restore] and [Meter.RestoreDay]
// load them back at startup; nothing here writes one row per response.
package egress

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
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
// which is what the dependency proxy answers and what every request
// resolves to on a service running with auth off. They share one budget,
// so an open route cannot be spent anonymously past the cap.
const AnonymousPrincipal = "anonymous"

// TeamPrincipal names a caller to a meter by its team and its token's name,
// because a token's name is free text that two teams may both choose.
func TeamPrincipal(team, name string) string {
	if team == "" {
		return name
	}
	return team + "/" + name
}

// OverflowPrincipal labels bytes served to a principal the meter had no
// room to track separately. They share one budget rather than an
// unbudgeted one, so reaching the cardinality cap tightens the meter
// instead of opening it. The fold lasts until the month rolls, which is
// when a meter drops the principals that spent nothing.
const OverflowPrincipal = "(overflow)"

// MaxPendingUsages bounds the closing-month totals a meter parks for a
// drain it is still waiting on. It sits above one month roll at the
// principal cap, so a drain that runs monthly never reaches it and one
// that has stopped entirely is capped rather than unbounded.
const MaxPendingUsages = 2 * MaxPrincipals

// MaxPrincipals bounds how many principals one meter tracks. A
// controller mints tokens under an operator's hand, so the cap sits far
// above a real deployment's principal count and exists to bound memory
// against a fleet that invents identities.
const MaxPrincipals = 10000

// Slot names a concurrency cap. Both slots bound how many responses one
// principal may hold open at once; they are counted apart because a live
// log stream lasts as long as its node and a download does not.
type Slot string

const (
	// SlotNone takes no slot, which is what a route that is metered for
	// bytes but must never be refused for concurrency passes.
	SlotNone Slot = ""
	// SlotDownload counts simultaneous metered downloads.
	SlotDownload Slot = "download"
	// SlotLogStream counts simultaneous live log streams.
	SlotLogStream Slot = "live log stream"
)

// ErrConcurrencyLimit matches every *ConcurrencyError under errors.Is.
var ErrConcurrencyLimit = errors.New("egress concurrency limit reached")

// ConcurrencyError refuses a response because the principal already
// holds its cap of that slot open.
type ConcurrencyError struct {
	Principal string
	Slot      Slot
	Limit     int
	// RetryAfter is how long a client should wait before retrying.
	RetryAfter time.Duration
	// Flag names the flag that raises the cap on the refusing service.
	Flag string
}

func (e *ConcurrencyError) Error() string {
	flag := e.Flag
	if flag == "" {
		flag = FlagMaxLogStreams
	}
	return fmt.Sprintf(
		"egress concurrency limit reached: %s already holds %d concurrent %ss, which is the cap, so this one is refused. "+
			"Let one finish and retry, or raise %s on the serving process",
		e.Principal, e.Limit, e.Slot, flag)
}

func (e *ConcurrencyError) Unwrap() error { return ErrConcurrencyLimit }

// SlotRetryAfter is the wait a refused response is told to observe. A
// stream ends when its node does and a download ends when its bytes run
// out, so a short retry is the honest answer for both.
const SlotRetryAfter = 30 * time.Second

// Config is a Meter's whole configuration. Every budget is off at zero,
// which is what a Sparkwing that was never given one serves.
type Config struct {
	// GlobalDailyAlarmBytes raises the alarm once the process has sent
	// this many bytes in a UTC day. It refuses nothing. Zero is off.
	GlobalDailyAlarmBytes int64
	// MaxStreamsPerPrincipal caps concurrent live log streams per
	// principal. Zero is unlimited.
	MaxStreamsPerPrincipal int
	// MaxDownloadsPerPrincipal caps concurrent metered downloads per
	// principal. A pool sharing one bearer is one principal, so the cap
	// is sized for the pool. Zero is unlimited.
	MaxDownloadsPerPrincipal int
	// Flags names the flags this service spells its budgets with, so a
	// refusal tells the operator which one to raise. The zero value uses
	// the unprefixed names.
	Flags FlagNames
	// Persisted marks a meter whose owner drains it with [Meter.Flush].
	// Only such a meter parks a month's closing totals; see
	// [Meter.WithPersistence], which sets the same thing after the fact.
	Persisted bool
}

// Budgeted reports whether any budget in this configuration applies.
func (c Config) Budgeted() bool {
	return c.GlobalDailyAlarmBytes > 0 || c.MaxStreamsPerPrincipal > 0 || c.MaxDownloadsPerPrincipal > 0
}

func (c Config) limitFor(slot Slot) int {
	if slot == SlotDownload {
		return c.MaxDownloadsPerPrincipal
	}
	return c.MaxStreamsPerPrincipal
}

func (c Config) flagFor(slot Slot) string {
	if slot == SlotDownload {
		return c.Flags.orDefault().MaxDownloads
	}
	return c.Flags.orDefault().MaxLogStreams
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
	Downloads  int    `json:"downloads"`
}

// State is the whole meter at a point in time, as the health route and
// the top-consumers view report it.
type State struct {
	DailyAlarmBytes          int64     `json:"daily_alarm_bytes"`
	MaxStreamsPerPrincipal   int       `json:"max_streams_per_principal"`
	MaxDownloadsPerPrincipal int       `json:"max_downloads_per_principal"`
	Month                    string    `json:"month"`
	Day                      string    `json:"day"`
	GlobalMonthBytes         int64     `json:"global_month_bytes"`
	GlobalDayBytes           int64     `json:"global_day_bytes"`
	Alarm                    bool      `json:"alarm"`
	AlarmSince               time.Time `json:"alarm_since,omitzero"`
	Refused                  uint64    `json:"refused_total"`
	Principals               int       `json:"principals"`
	// Persisted reports whether this meter's owner drains it, which is what
	// decides whether a closing month is parked or dropped.
	Persisted bool `json:"persisted"`
	// Pending is the closing-month totals waiting for the next drain, and
	// PendingDropped how many the backlog cap has discarded.
	Pending        int              `json:"pending,omitempty"`
	PendingDropped uint64           `json:"pending_dropped,omitempty"`
	Top            []PrincipalState `json:"top,omitempty"`
}

// TopConsumers is how many principals [Meter.State] reports.
const TopConsumers = 10

type principalCounters struct {
	month      string
	monthBytes int64
	day        string
	dayBytes   int64
	streams    int
	downloads  int
	dirty      bool
}

type pendingUsage struct {
	id uint64
	Usage
}

func (st *principalCounters) idle() bool {
	return st.monthBytes == 0 && st.dayBytes == 0 &&
		st.streams == 0 && st.downloads == 0 && !st.dirty
}

func (st *principalCounters) slots(slot Slot) int {
	if slot == SlotDownload {
		return st.downloads
	}
	return st.streams
}

func (st *principalCounters) hold(slot Slot, delta int) {
	if slot == SlotDownload {
		st.downloads += delta
		return
	}
	st.streams += delta
}

// Meter counts bytes sent per principal and for the process as a whole.
// Safe for concurrent use.
type Meter struct {
	cfg    Config
	logger *slog.Logger

	mu         sync.Mutex
	now        func() time.Time
	principals map[string]*principalCounters
	// safety: a month roll zeroes a principal's counter, so the closing
	// total is parked here for the next Flush rather than lost between
	// the roll and the flush that would have persisted it. Only a meter
	// whose owner drains it parks anything; see [Meter.WithPersistence].
	persists       bool
	pending        []pendingUsage
	nextPendingID  uint64
	pendingDropped uint64
	pendingWarned  bool
	month          string
	monthBytes     int64
	day            string
	dayBytes       int64
	dayDirty       bool
	alarm          bool
	alarmSince     time.Time
	refused        uint64
	overflowed     bool
}

// New builds a Meter on cfg. A zero Config counts every byte and
// refuses nothing, which is what a deployment with no budget wants: the
// health route and the top-consumers view still answer.
func New(cfg Config) *Meter {
	return &Meter{
		cfg:        cfg,
		persists:   cfg.Persisted,
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

// WithPersistence marks this meter as one whose owner drains it with
// [Meter.Flush] and persists what comes out. Only such a meter parks a
// month's closing totals for the next drain; a meter nobody drains would
// otherwise accumulate one entry per principal per month roll forever.
func (m *Meter) WithPersistence() *Meter {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persists = true
	return m
}

// Config returns the budgets this meter was built on.
func (m *Meter) Config() Config { return m.cfg }

// Record adds n bytes served to principal through class, whatever the
// budgets say.
func (m *Meter) Record(principal string, class Class, n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	now := m.now().UTC()
	key, st := m.counters(principal, now)
	st.monthBytes += n
	st.dayBytes += n
	st.dirty = true
	m.monthBytes += n
	m.dayBytes += n
	m.dayDirty = true
	raised := m.raiseAlarmLocked(now)
	// safety: every value the log line needs is copied under the lock,
	// because reading the principal map again after the unlock races the
	// next charge's write to it.
	day, total, principalDay := m.day, m.dayBytes, st.dayBytes
	m.mu.Unlock()

	// safety: the line names the principal whose bytes crossed the threshold
	// and what it sent today, because the alarm is all an operator gets.
	if raised {
		m.logger.Warn("egress daily alarm",
			"day", day,
			"day_bytes", total,
			"threshold_bytes", m.cfg.GlobalDailyAlarmBytes,
			"principal", key,
			"principal_day_bytes", principalDay,
			"class", string(class))
	}
}

// Serve returns a ResponseWriter that counts what the handler sends to
// principal. Bytes count only on a 2xx response to a request whose method
// carries a body, because net/http discards what a handler writes to a
// HEAD and an error body is not a download. It preserves the flush, hijack,
// and deadline control a streaming handler reaches for.
func (m *Meter) Serve(w http.ResponseWriter, r *http.Request, principal string, class Class) http.ResponseWriter {
	if m == nil {
		return w
	}
	return m.serve(w, r, principal, class)
}

// Handle runs next with a writer from [Meter.Serve]. A nil meter runs next
// uncounted.
func (m *Meter) Handle(w http.ResponseWriter, r *http.Request, principal string, class Class, next http.Handler) {
	next.ServeHTTP(m.Serve(w, r, principal, class), r)
}

func (m *Meter) serve(w http.ResponseWriter, r *http.Request, principal string, class Class) http.ResponseWriter {
	counting := &countingWriter{
		ResponseWriter: w,
		meter:          m,
		principal:      principal,
		class:          class,
		bodyless:       Bodyless(r),
		status:         http.StatusOK,
	}
	// safety: a stream handler asks whether its writer flushes or hijacks
	// before it commits to a protocol, so the wrapper answers those
	// questions the same way the writer underneath it would.
	flusher, canFlush := w.(http.Flusher)
	hijacker, canHijack := w.(http.Hijacker)
	switch {
	case canFlush && canHijack:
		return &flushingHijackingWriter{countingWriter: counting, flusher: flusher, hijacker: hijacker}
	case canFlush:
		return &flushingWriter{countingWriter: counting, flusher: flusher}
	case canHijack:
		return &hijackingWriter{countingWriter: counting, hijacker: hijacker}
	default:
		return counting
	}
}

// Bodyless reports whether a request's method makes the server discard
// whatever the handler writes, so a caller can skip the work of
// producing bytes nobody is charged for.
func Bodyless(r *http.Request) bool {
	return r != nil && r.Method == http.MethodHead
}

// Open reserves one of principal's slots and returns the release the
// caller defers. A principal already at the cap gets a
// *ConcurrencyError and a release that does nothing.
func (m *Meter) Open(principal string, slot Slot) (func(), error) {
	if m == nil || slot == SlotNone || m.cfg.limitFor(slot) <= 0 {
		return func() {}, nil
	}
	limit := m.cfg.limitFor(slot)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	key, st := m.counters(principal, now)
	if st.slots(slot) >= limit {
		m.refused++
		return func() {}, &ConcurrencyError{
			Principal:  key,
			Slot:       slot,
			Limit:      limit,
			RetryAfter: SlotRetryAfter,
			Flag:       m.cfg.flagFor(slot),
		}
	}
	st.hold(slot, 1)
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if held := m.principals[key]; held != nil && held.slots(slot) > 0 {
				held.hold(slot, -1)
			}
		})
	}, nil
}

// Flush gives persist the usages whose byte totals moved and acknowledges
// only the exact snapshot that persist commits. A failed persistence keeps
// every usage eligible for the next flush, while bytes recorded during a
// successful persistence remain eligible as a newer total.
func (m *Meter) Flush(persist func([]Usage) error) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	pending := append([]pendingUsage(nil), m.pending...)
	out := make([]Usage, 0, len(pending)+len(m.principals))
	for _, usage := range pending {
		out = append(out, usage.Usage)
	}
	for name, st := range m.principals {
		if !st.dirty {
			continue
		}
		out = append(out, Usage{Principal: name, Month: st.month, Bytes: st.monthBytes})
	}
	m.mu.Unlock()
	if len(out) == 0 {
		return nil
	}
	slices.SortFunc(out, func(a, b Usage) int {
		if c := compareStrings(a.Month, b.Month); c != 0 {
			return c
		}
		return compareStrings(a.Principal, b.Principal)
	})
	if err := persist(append([]Usage(nil), out...)); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, usage := range out {
		st := m.principals[usage.Principal]
		if st != nil && st.dirty && st.month == usage.Month && st.monthBytes == usage.Bytes {
			st.dirty = false
		}
	}
	if len(pending) == 0 {
		return nil
	}
	acknowledged := make(map[uint64]struct{}, len(pending))
	for _, usage := range pending {
		acknowledged[usage.id] = struct{}{}
	}
	kept := m.pending[:0]
	removed := false
	for _, usage := range m.pending {
		if _, ok := acknowledged[usage.id]; ok {
			removed = true
		} else {
			kept = append(kept, usage)
		}
	}
	m.pending = kept
	if removed {
		m.pendingWarned = false
	}
	return nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
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
		_, st := m.counters(u.Principal, now)
		if u.Bytes <= st.monthBytes {
			continue
		}
		m.monthBytes += u.Bytes - st.monthBytes
		st.monthBytes = u.Bytes
	}
}

// DayUsage is the process's total for one UTC day, as a service persists
// and reloads it so a restart does not reopen the daily cap.
type DayUsage struct {
	// Day is the UTC day the bytes fell in, as "2006-01-02".
	Day   string
	Bytes int64
	// Month is Day's UTC month, as "2006-01", and MonthBytes the process
	// total for it, so a restart later in the month resumes it too.
	Month      string `json:",omitempty"`
	MonthBytes int64  `json:",omitempty"`
}

// FlushDay gives persist today's process total when it moved since the
// last successful FlushDay, and acknowledges it only when persist
// succeeds and nothing was charged meanwhile.
func (m *Meter) FlushDay(persist func(DayUsage) error) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.rollGlobal(m.now().UTC())
	if !m.dayDirty {
		m.mu.Unlock()
		return nil
	}
	usage := DayUsage{Day: m.day, Bytes: m.dayBytes, Month: m.month, MonthBytes: m.monthBytes}
	m.mu.Unlock()
	if err := persist(usage); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.day == usage.Day && m.dayBytes == usage.Bytes && m.monthBytes == usage.MonthBytes {
		m.dayDirty = false
	}
	return nil
}

// RestoreDay loads a persisted process total, so a restarted service
// resumes the day and the month where it left off rather than reopening the
// daily cap or restarting the month's count. A total for another day or
// month is ignored for that period, and one below what this process has
// already counted never lowers it.
func (m *Meter) RestoreDay(u DayUsage) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	m.rollGlobal(now)
	if u.Month == m.month && u.MonthBytes > m.monthBytes {
		m.monthBytes = u.MonthBytes
	}
	if u.Day != m.day {
		return
	}
	if u.Bytes <= m.dayBytes {
		return
	}
	m.dayBytes = u.Bytes
	m.raiseAlarmLocked(now)
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
		DailyAlarmBytes:          m.cfg.GlobalDailyAlarmBytes,
		MaxStreamsPerPrincipal:   m.cfg.MaxStreamsPerPrincipal,
		MaxDownloadsPerPrincipal: m.cfg.MaxDownloadsPerPrincipal,
		Month:                    m.month,
		Day:                      m.day,
		GlobalMonthBytes:         m.monthBytes,
		GlobalDayBytes:           m.dayBytes,
		Alarm:                    m.alarm,
		AlarmSince:               m.alarmSince,
		Refused:                  m.refused,
		Principals:               len(m.principals),
		Persisted:                m.persists,
		PendingDropped:           m.pendingDropped,
	}
	for name, st := range m.principals {
		// safety: the view puts a principal's day beside the process's, so
		// this rolls each one to now; without it a read after midnight
		// showed yesterday's per-principal bytes against a zeroed global.
		m.rollPrincipal(name, st, now)
		if st.monthBytes == 0 && st.dayBytes == 0 && st.streams == 0 && st.downloads == 0 {
			continue
		}
		out.Top = append(out.Top, PrincipalState{
			Principal:  name,
			MonthBytes: st.monthBytes,
			DayBytes:   st.dayBytes,
			Streams:    st.streams,
			Downloads:  st.downloads,
		})
	}
	slices.SortFunc(out.Top, func(a, b PrincipalState) int {
		if a.MonthBytes != b.MonthBytes {
			return int(sign(b.MonthBytes - a.MonthBytes))
		}
		return compareStrings(a.Principal, b.Principal)
	})
	if len(out.Top) > TopConsumers {
		out.Top = out.Top[:TopConsumers]
	}
	out.Pending = len(m.pending)
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

// safety: the returned name is the one the principal is tracked under,
// which is not the name the caller passed when the fold or the anonymous
// mapping renamed it; every caller must label with what comes back here.
func (m *Meter) counters(principal string, now time.Time) (string, *principalCounters) {
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
	m.rollPrincipal(key, st, now)
	return key, st
}

func (m *Meter) rollPrincipal(name string, st *principalCounters, now time.Time) {
	month, day := now.Format("2006-01"), now.Format("2006-01-02")
	if st.month != month {
		if st.dirty {
			m.park(Usage{Principal: name, Month: st.month, Bytes: st.monthBytes})
			st.dirty = false
		}
		st.month = month
		st.monthBytes = 0
	}
	if st.day != day {
		st.day = day
		st.dayBytes = 0
	}
}

// safety: rolling the day total also clears the alarm, so nobody stays
// paged for yesterday's bytes; a month roll drops principals that spent
// nothing and hold nothing open, so the cardinality cap is not permanent.
func (m *Meter) rollGlobal(now time.Time) {
	month, day := now.Format("2006-01"), now.Format("2006-01-02")
	if m.month != month {
		m.month = month
		m.monthBytes = 0
		m.evictIdle(now)
	}
	if m.day != day {
		m.day = day
		m.dayBytes = 0
		m.dayDirty = false
		m.alarm = false
		m.alarmSince = time.Time{}
	}
}

// safety: a meter nobody drains parks nothing, and one whose drain has
// stopped drops its oldest entry rather than growing without bound; the
// store keeps a high-water mark, so a dropped entry costs history and
// never hands back spend.
func (m *Meter) park(u Usage) {
	if !m.persists {
		return
	}
	if len(m.pending) >= MaxPendingUsages {
		m.pending = m.pending[1:]
		m.pendingDropped++
		if !m.pendingWarned {
			m.pendingWarned = true
			m.logger.Warn("egress usage backlog full",
				"pending", len(m.pending), "limit", MaxPendingUsages)
		}
	}
	m.nextPendingID++
	m.pending = append(m.pending, pendingUsage{id: m.nextPendingID, Usage: u})
}

func (m *Meter) evictIdle(now time.Time) {
	for name, st := range m.principals {
		m.rollPrincipal(name, st, now)
		if st.idle() {
			delete(m.principals, name)
		}
	}
	if len(m.principals) < MaxPrincipals {
		m.overflowed = false
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

type countingWriter struct {
	http.ResponseWriter
	meter     *Meter
	principal string
	class     Class
	// safety: true when the server discards this response body, so nothing
	// written through the wrapper reaches the wire and nothing is charged.
	bodyless bool
	status   int
	wrote    bool
}

func (w *countingWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.wrote = true
	n, err := w.ResponseWriter.Write(p)
	if w.charges() {
		w.meter.Record(w.principal, w.class, int64(n))
	}
	return n, err
}

// safety: net/http drops a HEAD response body and an error body is not a
// download, so neither is counted; counting a HEAD let eight of them latch
// the daily alarm with nothing on the wire.
func (w *countingWriter) charges() bool {
	return !w.bodyless && w.status >= 200 && w.status < 300
}

// Unwrap hands the underlying writer to [http.NewResponseController],
// so a streaming handler keeps its flush and its write deadline.
func (w *countingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type flushingWriter struct {
	*countingWriter
	flusher http.Flusher
}

func (w *flushingWriter) Flush() { w.flusher.Flush() }

type hijackingWriter struct {
	*countingWriter
	hijacker http.Hijacker
}

func (w *hijackingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return w.hijacker.Hijack() }

type flushingHijackingWriter struct {
	*countingWriter
	flusher  http.Flusher
	hijacker http.Hijacker
}

func (w *flushingHijackingWriter) Flush() { w.flusher.Flush() }

func (w *flushingHijackingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijacker.Hijack()
}
