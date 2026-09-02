package logs

import (
	"context"
	"io/fs"
	"os"
	"sync"
	"time"
)

// TruncationMarker is the line the service appends once to a node log
// when the node or run byte cap stops it accepting more bytes. It is
// the only content the service writes that a runner did not send.
const TruncationMarker = "[sparkwing-logs] truncated: byte cap reached\n"

// Limits bounds the disk and search work one logs service will spend
// on callers it has already authenticated. Every field is independent
// and a zero field removes that bound; [DefaultLimits] carries the
// values the shipped service uses.
type Limits struct {
	// MaxNodeBytes caps the stored size of one node's log file.
	MaxNodeBytes int64
	// MaxRunBytes caps the stored size of all node logs in one run.
	MaxRunBytes int64
	// MinFreeBytes is the free space below which appends are rejected
	// with 507 rather than filling the volume.
	MinFreeBytes uint64
	// Retention is how long a run's logs survive after their last
	// write. Zero keeps them forever.
	Retention time.Duration
	// SweepInterval is how often the retention sweeper runs.
	SweepInterval time.Duration
	// SearchMaxBytes caps the bytes one search request reads.
	SearchMaxBytes int64
	// SearchTimeout caps how long one search request scans for.
	SearchTimeout time.Duration
}

// DefaultLimits returns the bounds a logs service uses when its
// operator sets none.
func DefaultLimits() Limits {
	return Limits{
		MaxNodeBytes:   64 << 20,
		MaxRunBytes:    1 << 30,
		MinFreeBytes:   512 << 20,
		Retention:      7 * 24 * time.Hour,
		SweepInterval:  time.Hour,
		SearchMaxBytes: 256 << 20,
		SearchTimeout:  10 * time.Second,
	}
}

// WithLimits replaces the server's resource bounds. Call it before
// [Server.Handler]; it is not safe to call on a serving Server.
func (s *Server) WithLimits(l Limits) *Server {
	s.limits = l
	return s
}

type appendPlan struct {
	write  []byte
	marker bool
}

// safety: room is the smaller of the node and run headroom, so one chatty node cannot spend the whole run budget.
func (s *Server) planAppend(nodeSize, runTotal int64, body []byte) appendPlan {
	room := int64(len(body))
	if limit := s.limits.MaxNodeBytes; limit > 0 && limit-nodeSize < room {
		room = limit - nodeSize
	}
	if limit := s.limits.MaxRunBytes; limit > 0 && limit-runTotal < room {
		room = limit - runTotal
	}
	if room <= 0 {
		return appendPlan{}
	}
	if room < int64(len(body)) {
		return appendPlan{write: body[:room], marker: true}
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

type freeSpaceProbe struct {
	mu      sync.Mutex
	checked time.Time
	free    uint64
	ok      bool
}

// perf: appends arrive per log line, so the statfs result is reused for a second rather than run on every one.
const freeSpaceProbeTTL = time.Second

func (s *Server) hasFreeSpace() bool {
	if s.limits.MinFreeBytes == 0 {
		return true
	}
	s.freeSpace.mu.Lock()
	defer s.freeSpace.mu.Unlock()
	now := time.Now()
	if now.Sub(s.freeSpace.checked) >= freeSpaceProbeTTL {
		s.freeSpace.free, _, s.freeSpace.ok = diskSpace(s.root)
		s.freeSpace.checked = now
	}
	if !s.freeSpace.ok {
		return true
	}
	return s.freeSpace.free >= s.limits.MinFreeBytes
}

// StartSweeper runs the retention sweep until ctx is done. It returns
// immediately when retention or the sweep interval is disabled.
func (s *Server) StartSweeper(ctx context.Context) {
	if s.limits.Retention <= 0 || s.limits.SweepInterval <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(s.limits.SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := s.SweepOnce(time.Now()); err != nil {
					s.logger.Error("logs sweep", "err", err)
				}
			}
		}
	}()
}

// SweepOnce removes every run directory whose most recent write is
// older than the configured retention, and reports how many it
// removed. Retention of zero removes nothing.
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
	cutoff := now.Add(-s.limits.Retention)
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
