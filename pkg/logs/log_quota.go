package logs

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

// A free team's logs are held to their share of the allowance where they
// are written. What a team holds is its archived objects, counted by the
// archive's store, plus what its runs on the volume hold beyond what the
// archive already has. The volume part is measured by walking the live runs
// after every archive pass and grows with every append in between, so it
// falls only when files are deleted and a byte is never released while it
// still exists.

// volumeUsage is each team's unarchived bytes on the volume.
type volumeUsage struct {
	mu    sync.Mutex
	teams map[string]int64
	// pending counts appends made while a measurement walks, which the
	// walk may have missed; nil when no walk runs.
	pending map[string]int64
}

func (v *volumeUsage) add(team string, n int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.teams[team] += n
	if v.pending != nil {
		v.pending[team] += n
	}
}

func (v *volumeUsage) team(team string) int64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.teams[team]
}

// safety: an append that lands during the walk may or may not be in what
// the walk read, so it is added again; the next walk takes the double count
// back, and until then the team is held to less, never more.
func (v *volumeUsage) begin() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pending = map[string]int64{}
}

func (v *volumeUsage) finish(walked map[string]int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for team, n := range v.pending {
		walked[team] += n
	}
	v.teams, v.pending = walked, nil
}

func (v *volumeUsage) abort() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pending = nil
}

func newLogQuota(store *teamblob.Store, volume *volumeUsage) *storagequota.Quota {
	return storagequota.New(storagequota.Options{
		Share: storagequota.LogShare,
		Used: func(team string) int64 {
			return volume.team(team) + store.Usage().Team(team).Bytes
		},
		Exempt: func(team string) bool { return team == "" || team == authwire.OperatorTeam },
	})
}

// unarchivedBytes is what a run's files on the volume hold past the sizes
// its last finished archive stored. A restored run matches the archive and
// holds none.
func unarchivedBytes(root *os.Root, runID string) (string, int64, error) {
	meta := readRunMeta(root, runID)
	files, err := runFiles(root, runID)
	if err != nil {
		return meta.Team, 0, err
	}
	committed := meta.committedSizes()
	var n int64
	for _, f := range files {
		n += max(f.size-committed[f.rel], 0)
	}
	return meta.Team, n, nil
}

// MeasureVolumeUsage walks the runs on the volume and replaces each team's
// unarchived byte count. The archiver calls it after every pass.
func (s *Server) MeasureVolumeUsage(ctx context.Context) error {
	if s.archive == nil {
		return nil
	}
	root, err := s.openRunsRoot()
	if err != nil {
		return err
	}
	defer s.closeRoot(root, "measure volume usage")
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return err
	}
	v := s.archive.volume
	v.begin()
	walked := map[string]int64{}
	for _, e := range entries {
		if ctx.Err() != nil {
			v.abort()
			return ctx.Err()
		}
		if !e.IsDir() || validateID(e.Name()) != nil {
			continue
		}
		team, n, err := unarchivedBytes(root, e.Name())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			v.abort()
			return err
		}
		walked[team] += n
	}
	v.finish(walked)
	return nil
}

// standingFromClaim reads the tier the controller answered a claim check
// with. An answer that names none leaves the standing the quota already
// holds, which for a team never answered for is the default free share.
func standingFromClaim(h http.Header) (storagequota.Standing, bool) {
	tier := storagequota.Tier(h.Get(storagequota.TierHeader))
	if tier == "" {
		return storagequota.Standing{}, false
	}
	allowance, err := strconv.ParseInt(h.Get(storagequota.AllowanceHeader), 10, 64)
	if err != nil || allowance < 0 {
		allowance = storagequota.DefaultAllowanceBytes
	}
	return storagequota.Standing{Tier: tier, AllowanceBytes: allowance}, true
}

func writeQuotaRefusal(w http.ResponseWriter, err error) {
	status := http.StatusRequestEntityTooLarge
	if errors.Is(err, storagequota.ErrPaused) {
		status = http.StatusPaymentRequired
	}
	http.Error(w, err.Error(), status)
}

// RestoreArchive restores the archive's per-team count and measures the
// volume, so the log share is judged against what the service already holds.
// ServeWith calls it before listening whenever the service authenticates
// teams, and refuses to start when it fails.
func (s *Server) RestoreArchive(ctx context.Context) error {
	a := s.archive
	if a == nil {
		return nil
	}
	if err := a.store.Restore(ctx, a.opts.UsageReconcile); err != nil {
		return err
	}
	if err := s.MeasureVolumeUsage(ctx); err != nil {
		return err
	}
	a.mu.Lock()
	a.restored = true
	a.mu.Unlock()
	return nil
}
