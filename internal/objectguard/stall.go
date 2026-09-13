package objectguard

import (
	"sort"
	"sync"
	"time"
)

// Stall is one object-store path that stopped making progress. A stalled
// path bills nothing more, which is the point, but it also delivers
// nothing more, so an operator has to hear about it.
type Stall struct {
	// Path names the loop that stopped, in words an operator reads.
	Path string `json:"path"`
	// Error is the failure it gave up on.
	Error string `json:"error"`
	// Since is when it gave up.
	Since time.Time `json:"since"`
}

var (
	stallMu sync.Mutex
	stalls  = map[string]Stall{}
)

// ReportStall records that path has stopped making progress, replacing
// any earlier report for the same path. Call ClearStall when it
// recovers.
func ReportStall(path string, err error) {
	if path == "" || err == nil {
		return
	}
	stallMu.Lock()
	defer stallMu.Unlock()
	if prior, ok := stalls[path]; ok && prior.Error == err.Error() {
		return
	}
	stalls[path] = Stall{Path: path, Error: err.Error(), Since: time.Now().UTC()}
}

// ClearStall forgets a path that is making progress again.
func ClearStall(path string) {
	stallMu.Lock()
	defer stallMu.Unlock()
	delete(stalls, path)
}

// Stalls lists every path currently stalled, ordered by path so the
// health route and its tests read the same way twice.
func Stalls() []Stall {
	stallMu.Lock()
	defer stallMu.Unlock()
	out := make([]Stall, 0, len(stalls))
	for _, s := range stalls {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
