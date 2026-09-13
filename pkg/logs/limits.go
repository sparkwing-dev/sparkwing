package logs

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
)

// TruncationMarker is the line the service appends once to a node log
// when the node or run byte cap stops it accepting more bytes.
const TruncationMarker = "[sparkwing-logs] truncated: byte cap reached\n"

// LineTruncationMarker replaces the tail of a line that ran past
// [Limits.MaxLineBytes]. The line before it is stored whole up to the
// cap.
const LineTruncationMarker = "[sparkwing-logs] truncated: line byte cap reached\n"

// BinaryDropMarker stands in for output the service refused to store
// because it reads as binary rather than text. It is written once per
// node log, and the output it replaces is discarded.
const BinaryDropMarker = "[sparkwing-logs] dropped: this node's output reads as binary, not text\n"

// Limits bounds the disk, memory and search work one logs service will
// spend on callers it has already authenticated. Every field is
// independent and a zero field removes that bound; [DefaultLimits]
// carries the values the shipped service uses.
type Limits struct {
	// MaxNodeBytes caps one node's legacy and attributed log substreams together.
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
	// MaxLineBytes caps one log line. A longer line is stored cut to
	// the cap with LineTruncationMarker in place of its tail. Zero, the
	// default, stores a line of any length.
	MaxLineBytes int64
	// BinaryRatio is the share of control bytes in one append above
	// which the whole append reads as binary and is dropped with
	// BinaryDropMarker. Zero, the default, stores every append whatever
	// it holds; 0.3 catches a binary a pipeline cats without flagging
	// text that carries the odd escape sequence.
	BinaryRatio float64
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

// MinLineBytes is the smallest usable line cap: a cut line carries
// [LineTruncationMarker] inside the cap, so a smaller one could store
// the marker and nothing else.
var MinLineBytes = int64(len(LineTruncationMarker)) + 1

// WithLimits replaces the server's resource bounds. A MaxLineBytes
// under [MinLineBytes] is raised to it, because a cap that cannot hold
// the marker and a byte of output is not the bound it names. Call it
// before [Server.Handler]; it is not safe to call on a serving Server.
func (s *Server) WithLimits(l Limits) *Server {
	if l.MaxLineBytes > 0 && l.MaxLineBytes < MinLineBytes {
		s.logger.Warn("logs store", "op", "limits",
			"max_line_bytes", l.MaxLineBytes, "raised_to", MinLineBytes,
			"err", "a line cap must leave room for the truncation marker")
		l.MaxLineBytes = MinLineBytes
	}
	s.limits = l
	return s
}

// StoreCeilingSubject names the logs service's own store in a refusal.
const StoreCeilingSubject = "the log store"

// StoreCeilingRemedy is the operator instruction a refused append ends
// with.
const StoreCeilingRemedy = "Delete a run with DELETE /api/v1/logs/{runID}, or set --retention " +
	"(SPARKWING_LOGS_RETENTION) so the sweeper does; both measure the store again, so appends " +
	"resume as soon as it is back under the ceiling. Raise --max-store-bytes or --max-store-objects " +
	"to accept more."

// WithStoreCeiling bounds the whole log store, not one node or run: at
// or above either ceiling the service refuses every append with 507
// until a measurement finds the store back under it. A cfg with no
// ceiling set, which is the default, never refuses an append.
//
// Call it before [Server.Handler]; it is not safe to call on a serving
// Server.
func (s *Server) WithStoreCeiling(cfg objectguard.CeilingConfig) *Server {
	cfg.Subject = StoreCeilingSubject
	cfg.Remedy = StoreCeilingRemedy
	s.ceiling = objectguard.NewCeiling(cfg)
	s.publishStoreCeiling(s.ceiling)
	return s
}

// StoreCeiling reports the whole-store ceiling and what the service has
// counted against it.
func (s *Server) StoreCeiling() objectguard.CeilingState { return s.ceiling.State() }

// MeasureStore walks the log store and folds the total into the
// ceiling, which is what the sweeper does on the reconciliation
// interval. It is a no-op while no ceiling is set.
func (s *Server) MeasureStore(ctx context.Context) error {
	return s.ceiling.ReconcileWith(ctx, func(ctx context.Context) (objectguard.Usage, error) {
		root, err := s.openRunsRoot()
		if err != nil {
			return objectguard.Usage{}, err
		}
		defer s.closeRoot(root, "measure store")
		return storeUsage(ctx, root)
	})
}

// safety: a close failure on a read-only walk changes no stored byte, so it is
// logged rather than returned over a measurement that succeeded.
func (s *Server) closeRoot(root *os.Root, op string) {
	if err := root.Close(); err != nil {
		s.logger.Error("logs store", "op", op, "err", err)
	}
}

// perf: one walk of the store per reconciliation interval, never per append,
// because the append path already knows what it wrote. The walk stops when its
// context does and reports the total as partial, so a shutdown or a deadline
// does not wait out a store of a million files.
func storeUsage(ctx context.Context, root *os.Root) (objectguard.Usage, error) {
	d, err := root.Open(".")
	if err != nil {
		return objectguard.Usage{}, err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return objectguard.Usage{}, err
	}
	usage := objectguard.Usage{ObservedAt: time.Now().UTC()}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		bytes, files, partial := treeUsage(ctx, root, e.Name())
		usage.Bytes += bytes
		usage.Objects += files
		if partial {
			usage.Partial = true
			break
		}
	}
	return usage, nil
}

func treeUsage(ctx context.Context, root *os.Root, path string) (bytes, files int64, partial bool) {
	// safety: the check sits on each directory rather than each file, so a deep
	// tree is abandoned promptly without a context read per log file.
	if ctx.Err() != nil {
		return 0, 0, true
	}
	info, err := root.Stat(path)
	if err != nil {
		return 0, 0, false
	}
	if !info.IsDir() {
		return info.Size(), 1, false
	}
	entries, err := readDirAt(root, path)
	if err != nil {
		return 0, 0, false
	}
	for _, entry := range entries {
		b, f, p := treeUsage(ctx, root, filepath.Join(path, entry.Name()))
		bytes += b
		files += f
		if p {
			return bytes, files, true
		}
	}
	return bytes, files, false
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
	mu          sync.Mutex
	total       int64
	seeded      bool
	inUse       int
	binaryNoted map[string]struct{}
}

// safety: the marker is written once per node log while the service holds the
// run's state, so a node that keeps sending binary costs one line, not one per
// append, and a text append between two binary ones does not earn a second.
func (rt *runTotal) noteBinary(node string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if _, seen := rt.binaryNoted[node]; seen {
		return false
	}
	if rt.binaryNoted == nil {
		rt.binaryNoted = make(map[string]struct{})
	}
	rt.binaryNoted[node] = struct{}{}
	return true
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

// safety: the line cap is applied to the body before the node and run budgets
// see it, so one unbroken line cannot spend a node's whole allowance. Every cut
// line carries the marker, and the marker counts against the cap, so a stored
// line never exceeds the bound the operator asked for.
func capLines(body []byte, limit int64) []byte {
	if limit <= 0 || int64(len(body)) <= limit {
		return body
	}
	keep := limit - int64(len(LineTruncationMarker))
	out := make([]byte, 0, len(body))
	for len(body) > 0 {
		line := body
		var rest []byte
		if i := bytes.IndexByte(body, '\n'); i >= 0 {
			line, rest = body[:i+1], body[i+1:]
		}
		switch {
		case int64(len(line)) <= limit:
			out = append(out, line...)
		case keep <= 0:
			// safety: a cap under the marker cannot carry one, which [Server.WithLimits]
			// prevents; the line is still cut so the bound holds.
			out = append(out, truncateRunes(line, limit-1)...)
			out = append(out, '\n')
		default:
			out = append(out, truncateRunes(line, keep)...)
			out = append(out, LineTruncationMarker...)
		}
		body = rest
	}
	return out
}

// safety: a line cut mid-rune would store a replacement character a reader
// cannot tell from the pipeline's own output, so the cut backs off to a
// boundary.
func truncateRunes(line []byte, limit int64) []byte {
	if limit <= 0 {
		return nil
	}
	if int64(len(line)) <= limit {
		return line
	}
	cut := int(limit)
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	if r, size := utf8.DecodeLastRune(line[:cut]); r == utf8.RuneError && size == 1 {
		cut--
	}
	if cut < 0 {
		cut = 0
	}
	return line[:cut]
}

// safety: a byte above 0x7f counts as binary evidence only when it is not part
// of a valid UTF-8 sequence, so text in any language is stored as sent while a
// gzip or tar blob, which is mostly invalid UTF-8, is caught.
func looksBinary(body []byte, ratio float64) bool {
	if ratio <= 0 || len(body) == 0 {
		return false
	}
	suspect := 0
	for i := 0; i < len(body); {
		b := body[i]
		switch {
		case b == '\t' || b == '\n' || b == '\v' || b == '\f' || b == '\r':
			i++
		case b < 0x20 || b == 0x7f:
			suspect++
			i++
		case b < 0x80:
			i++
		default:
			r, size := utf8.DecodeRune(body[i:])
			if r == utf8.RuneError && size <= 1 {
				suspect++
				i++
				continue
			}
			i += size
		}
	}
	return float64(suspect)/float64(len(body)) > ratio
}

type appendPlan struct {
	write  []byte
	marker bool
}

// safety: room is the smaller of the node and run headroom, so one chatty node cannot spend the whole run budget.
func (s *Server) planAppend(root *os.Root, runID, nodeID string, rt *runTotal, body []byte) appendPlan {
	want := int64(len(body))
	if limit := s.limits.MaxNodeBytes; limit > 0 {
		if room := limit - logicalNodeSize(root, runID, nodeID); room < want {
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
	total, _ := logTreeUsage(root, runID)
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
		s.sweepCtx.Store(&ctx)
		s.startStoreCeiling(ctx)
		return
	}
	s.sweepCtx.Store(&ctx)
	if probing {
		s.refreshFreeSpace()
	}
	s.startStoreCeiling(ctx)
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

// safety: the sweeper's own context, so a measurement it triggers stops when the
// service does; a direct SweepOnce call outside the sweeper measures unbounded.
func (s *Server) sweepContext() context.Context {
	if ctx := s.sweepCtx.Load(); ctx != nil {
		return *ctx
	}
	return context.Background()
}

// safety: the first measurement runs before the service accepts appends, so a
// store already over its ceiling refuses from the first request rather than
// after the first interval.
func (s *Server) startStoreCeiling(ctx context.Context) {
	if !s.ceiling.Enforced() {
		return
	}
	if err := s.MeasureStore(ctx); err != nil {
		s.logger.Error("logs store", "op", "measure store", "err", err)
	}
	interval := s.ceiling.Reconcile()
	if interval <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.MeasureStore(ctx); err != nil {
					s.logger.Error("logs store", "op", "measure store", "err", err)
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
	ceilingSweep := false
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
		ceilingSweep = true
	}
	// safety: a sweep that deleted runs makes the counters wrong in the caller's
	// favor, so the store is measured again rather than staying frozen on bytes
	// that are gone.
	if ceilingSweep && s.ceiling.Enforced() {
		if err := s.MeasureStore(s.sweepContext()); err != nil {
			s.logger.Error("logs store", "op", "measure store", "err", err)
		}
	}
	return removed, nil
}

func newestWrite(root *os.Root, dir fs.DirEntry) time.Time {
	_, newest := logTreeUsage(root, dir.Name())
	return newest
}

func logTreeUsage(root *os.Root, path string) (int64, time.Time) {
	info, err := root.Stat(path)
	if err != nil {
		return 0, time.Time{}
	}
	newest := info.ModTime()
	if !info.IsDir() {
		return info.Size(), newest
	}
	entries, err := readDirAt(root, path)
	if err != nil {
		return 0, newest
	}
	var total int64
	for _, entry := range entries {
		size, modified := logTreeUsage(root, filepath.Join(path, entry.Name()))
		total += size
		if modified.After(newest) {
			newest = modified
		}
	}
	return total, newest
}
