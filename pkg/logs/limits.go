package logs

import (
	"context"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// TruncationMarker is the line the service appends once to a node log
// when the node or run byte cap stops it accepting more bytes. It is
// the only content the service writes that a runner did not send.
const TruncationMarker = "[sparkwing-logs] truncated: byte cap reached\n"

// Limits bounds the disk, memory and search work one logs service will
// spend on callers it has already authenticated. Every field is
// independent and a zero field removes that bound; [DefaultLimits]
// carries the values the shipped service uses.
type Limits struct {
	// MaxNodeBytes caps the stored size of one node's log file.
	MaxNodeBytes int64
	// MaxRunBytes caps the stored size of all node logs in one run.
	MaxRunBytes int64
	// MaxInFlightBytes caps the request-body bytes all appends may
	// hold in memory at once; appends over it are refused with 503.
	MaxInFlightBytes int64
	// MinFreeBytes is the free space below which appends are rejected
	// with 507 rather than filling the volume.
	MinFreeBytes uint64
	// Retention is how long a run's logs survive after their last
	// write. Zero, the default, keeps them forever.
	Retention time.Duration
	// SweepInterval is how often the retention sweeper runs.
	SweepInterval time.Duration
	// SearchMaxBytes caps the bytes one search request reads.
	SearchMaxBytes int64
	// SearchTimeout caps how long one search request scans for.
	SearchTimeout time.Duration
}

// DefaultLimits returns the bounds a logs service uses when its
// operator sets none. Retention is absent: deleting stored history is
// an operator decision, so the sweeper stays off until Retention is
// set.
func DefaultLimits() Limits {
	return Limits{
		MaxNodeBytes:     64 << 20,
		MaxRunBytes:      1 << 30,
		MaxInFlightBytes: 32 << 20,
		MinFreeBytes:     512 << 20,
		SweepInterval:    time.Hour,
		SearchMaxBytes:   256 << 20,
		SearchTimeout:    10 * time.Second,
	}
}

// WithLimits replaces the server's resource bounds. Call it before
// [Server.Handler]; it is not safe to call on a serving Server.
func (s *Server) WithLimits(l Limits) *Server {
	s.limits = l
	return s
}

type inFlightBytes struct {
	mu   sync.Mutex
	held int64
}

// safety: the reservation is taken before the body is read, so concurrent appends cannot outgrow the pod's memory.
func (b *inFlightBytes) reserve(n, limit int64) bool {
	if limit <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held+n > limit {
		return false
	}
	b.held += n
	return true
}

func (b *inFlightBytes) release(n, limit int64) {
	if limit <= 0 {
		return
	}
	b.mu.Lock()
	b.held -= n
	if b.held < 0 {
		b.held = 0
	}
	b.mu.Unlock()
}

// safety: a bound on tracked runs keeps the counters from growing with a caller's run ids.
const maxTrackedRuns = 4096

type runTotals struct {
	mu    sync.Mutex
	byRun map[string]*runTotal
}

type runTotal struct {
	mu     sync.Mutex
	total  int64
	seeded bool
	inUse  int
}

func (t *runTotals) acquire(runID string) *runTotal {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byRun == nil {
		t.byRun = make(map[string]*runTotal)
	}
	rt, ok := t.byRun[runID]
	if ok {
		rt.inUse++
		return rt
	}
	if len(t.byRun) >= maxTrackedRuns {
		for id, idle := range t.byRun {
			if idle.inUse == 0 {
				delete(t.byRun, id)
			}
		}
	}
	rt = &runTotal{}
	t.byRun[runID] = rt
	rt.inUse++
	return rt
}

func (t *runTotals) release(rt *runTotal) {
	t.mu.Lock()
	rt.inUse--
	t.mu.Unlock()
}

func (t *runTotals) forget(runID string) {
	t.mu.Lock()
	delete(t.byRun, runID)
	t.mu.Unlock()
}

// safety: seeding and reserving share one lock, so two appends cannot both spend the last byte of the run cap.
func (rt *runTotal) reserve(root *os.Root, runID string, want, limit int64) int64 {
	if limit <= 0 {
		return want
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.seeded {
		rt.total = runTotalBytes(root, runID)
		rt.seeded = true
	}
	room := limit - rt.total
	if room <= 0 {
		return 0
	}
	if want > room {
		want = room
	}
	rt.total += want
	return want
}

func (rt *runTotal) add(n int64) {
	rt.mu.Lock()
	rt.total += n
	rt.mu.Unlock()
}

func (rt *runTotal) unreserve(n int64) {
	rt.mu.Lock()
	rt.total -= n
	if rt.total < 0 {
		rt.total = 0
	}
	rt.mu.Unlock()
}

type appendPlan struct {
	write  []byte
	marker bool
}

// safety: room is the smaller of the node and run headroom, so one chatty node cannot spend the whole run budget.
func (s *Server) planAppend(root *os.Root, runID, name string, rt *runTotal, body []byte) appendPlan {
	want := int64(len(body))
	if limit := s.limits.MaxNodeBytes; limit > 0 {
		if room := limit - nodeSize(root, name); room < want {
			want = room
		}
	}
	if want <= 0 {
		return appendPlan{}
	}
	granted := rt.reserve(root, runID, want, s.limits.MaxRunBytes)
	if granted <= 0 {
		return appendPlan{}
	}
	if granted < int64(len(body)) {
		return appendPlan{write: body[:granted], marker: true}
	}
	return appendPlan{write: body}
}

func nodeSize(root *os.Root, name string) int64 {
	info, err := root.Stat(name)
	if err != nil {
		return 0
	}
	return info.Size()
}

func runTotalBytes(root *os.Root, runID string) int64 {
	entries, err := readRunDir(root, runID)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total
}

type freeSpaceState struct {
	free    uint64
	ok      bool
	checked time.Time
}

type freeSpaceProbe struct {
	state      atomic.Pointer[freeSpaceState]
	refreshing atomic.Bool
}

// perf: appends arrive per log line, so the statfs runs on a timer off the request path rather than on every one.
const freeSpaceProbeTTL = time.Second

func (s *Server) refreshFreeSpace() {
	free, _, ok := s.diskSpace(s.root)
	prev := s.freeSpace.state.Load()
	if !ok && (prev == nil || prev.ok) {
		s.logger.Error("logs store", "op", "free space probe",
			"err", "storage volume is not measurable; appends rejected until it is")
	}
	s.freeSpace.state.Store(&freeSpaceState{free: free, ok: ok, checked: time.Now()})
}

func (s *Server) refreshFreeSpaceAsync() {
	if !s.freeSpace.refreshing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.freeSpace.refreshing.Store(false)
		s.refreshFreeSpace()
	}()
}

func (s *Server) hasFreeSpace() bool {
	if s.limits.MinFreeBytes == 0 {
		return true
	}
	st := s.freeSpace.state.Load()
	if st == nil {
		s.refreshFreeSpace()
		st = s.freeSpace.state.Load()
	} else if time.Since(st.checked) >= freeSpaceProbeTTL {
		s.refreshFreeSpaceAsync()
	}
	// safety: a volume the service cannot measure fails closed, so a broken probe does not remove the floor.
	if !st.ok {
		return false
	}
	return st.free >= s.limits.MinFreeBytes
}

// StartSweeper keeps the free-space probe fresh and runs the retention
// sweep until ctx is done. The sweep half stays idle while retention or
// the sweep interval is disabled.
func (s *Server) StartSweeper(ctx context.Context) {
	probing := s.limits.MinFreeBytes > 0
	sweeping := s.limits.Retention > 0 && s.limits.SweepInterval > 0
	if !probing && !sweeping {
		return
	}
	if probing {
		s.refreshFreeSpace()
	}
	go func() {
		var probe, sweep <-chan time.Time
		if probing {
			t := time.NewTicker(freeSpaceProbeTTL)
			defer t.Stop()
			probe = t.C
		}
		if sweeping {
			t := time.NewTicker(s.limits.SweepInterval)
			defer t.Stop()
			sweep = t.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-probe:
				s.refreshFreeSpace()
			case <-sweep:
				if _, err := s.SweepOnce(time.Now()); err != nil {
					s.logger.Error("logs sweep", "err", err)
				}
			}
		}
	}()
}

// SweepOnce removes every run directory whose most recent write is
// older than the configured retention by a further sweep interval, and
// reports how many it removed. Retention of zero removes nothing.
func (s *Server) SweepOnce(now time.Time) (int, error) {
	if s.limits.Retention <= 0 {
		return 0, nil
	}
	root, err := s.openRunsRoot()
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	d, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return 0, err
	}
	// safety: a run written within a sweep of the cutoff waits one more, so a live append is not unlinked under it.
	cutoff := now.Add(-s.limits.Retention).Add(-s.limits.SweepInterval)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if newestWrite(root, e).After(cutoff) {
			continue
		}
		if err := root.RemoveAll(e.Name()); err != nil {
			s.logger.Error("logs sweep", "op", "remove run dir", "err", err)
			continue
		}
		s.runTotals.forget(e.Name())
		removed++
	}
	return removed, nil
}

func newestWrite(root *os.Root, dir fs.DirEntry) time.Time {
	newest := time.Time{}
	if info, err := dir.Info(); err == nil {
		newest = info.ModTime()
	}
	entries, err := readRunDir(root, dir.Name())
	if err != nil {
		return newest
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}
