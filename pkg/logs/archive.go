package logs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

// The archive keeps finished runs in an object store and the volume
// holds only live ones. Appends always land on the volume, where a node's
// log is one file that grows in place; object stores have no append, and
// one object per append is a request per line. A run nobody has written
// for ArchiveOptions.Idle is uploaded as one object per log file under
//
//	teams/<team>/runs/<runID>/<file>
//
// and removed from the volume. A read or append of an archived run
// restores it to the volume first, so every read path is the volume's.
//
// Two small index objects per run, in the store's operator namespace,
// make that cheap: index/runs/<runID>.json names the run's team and its
// files, so a restore or a delete never lists, and
// index/days/<date>/<runID> lets retention find expired runs one day's
// listing at a time instead of listing the whole store.
const (
	runMetaFile   = ".sparkwing-run"
	rehydrateDir  = "rehydrate"
	indexRunsRel  = "index/runs/"
	indexDaysRel  = "index/days/"
	dayLayout     = "2006-01-02"
	absentTTL     = 10 * time.Minute
	maxAbsentRuns = 10000

	// DefaultArchiveIdle is how long a run goes unwritten before it moves
	// to the object store. Long enough that a node paused between steps
	// is not uploaded mid-run; a run written again later is restored and
	// uploaded again.
	DefaultArchiveIdle = 10 * time.Minute
	// DefaultArchiveInterval is how often the archiver looks for idle
	// runs. Looking walks the volume and sends no request.
	DefaultArchiveInterval = time.Minute
	// DefaultArchiveRetention is the retention a logs service with an
	// archive starts with when the operator named none.
	DefaultArchiveRetention = 30 * 24 * time.Hour
	// DefaultPruneInterval is how often retention lists the day index,
	// one LIST request when nothing has expired.
	DefaultPruneInterval = time.Hour
	// minArchiveBackoff and maxArchiveBackoff bound the wait after a
	// failed pass: the floor keeps full jitter from retrying at once, and
	// the jittered part doubles per failure up to the cap.
	minArchiveBackoff = 30 * time.Second
	maxArchiveBackoff = 10 * time.Minute
)

// ArchiveOptions configures the object-store tier.
type ArchiveOptions struct {
	Store *teamblob.Store
	// Idle, Interval and PruneInterval default to DefaultArchiveIdle,
	// DefaultArchiveInterval and DefaultPruneInterval.
	Idle          time.Duration
	Interval      time.Duration
	PruneInterval time.Duration
	// UsageReconcile is how often the per-team count is replaced by a
	// listing of the store. Zero lists only when no saved count exists.
	UsageReconcile time.Duration
}

type archive struct {
	store *teamblob.Store
	opts  ArchiveOptions
	locks sync.Map // runID -> *runLock

	volume *volumeUsage
	// quota holds each team's appends to its log share, counted over the
	// store and the volume.
	quota *storagequota.Quota

	mu        sync.Mutex
	absent    map[string]time.Time
	failures  int
	retryAt   time.Time
	lastPrune time.Time
	backoff   objectguard.Backoff
}

type runLock struct {
	// rw is held shared by every request on the run and exclusively by
	// the archiver and a delete, so a run never leaves the volume under
	// a reader or a writer.
	rw sync.RWMutex
	// hydrate serializes restores of one run.
	hydrate sync.Mutex
}

// runMeta sits beside a run's logs on the volume.
type runMeta struct {
	Team string `json:"team,omitempty"`
	// ArchivedAt is when the files last matched the store; a file
	// modified after it has to be uploaded again.
	ArchivedAt time.Time      `json:"archived_at,omitzero"`
	Files      []archivedFile `json:"files,omitempty"`
	// Uploaded and IndexDigest record an archive in progress: the objects
	// that already landed, by size, and the index body already written,
	// so a retry sends only what is missing.
	Uploaded    map[string]int64 `json:"uploaded,omitempty"`
	IndexDigest string           `json:"index_digest,omitempty"`
}

type archivedFile struct {
	Rel  string `json:"rel"`
	Size int64  `json:"size"`
}

type runIndex struct {
	Team      string         `json:"team,omitempty"`
	LastWrite time.Time      `json:"last_write"`
	Files     []archivedFile `json:"files"`
}

// WithArchive moves idle runs to opts.Store. Call it before
// [Server.Handler]; it is not safe to call on a serving Server.
func (s *Server) WithArchive(opts ArchiveOptions) *Server {
	if opts.Store == nil {
		s.archive = nil
		return s
	}
	if opts.Idle <= 0 {
		opts.Idle = DefaultArchiveIdle
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultArchiveInterval
	}
	if opts.PruneInterval <= 0 {
		opts.PruneInterval = DefaultPruneInterval
	}
	volume := &volumeUsage{teams: map[string]int64{}}
	s.archive = &archive{
		store:   opts.Store,
		opts:    opts,
		absent:  map[string]time.Time{},
		backoff: objectguard.Backoff{Base: 30 * time.Second, Max: maxArchiveBackoff},
		volume:  volume,
		quota:   newLogQuota(opts.Store, volume),
	}
	return s
}

func (a *archive) lock(runID string) *runLock {
	l, _ := a.locks.LoadOrStore(runID, &runLock{})
	return l.(*runLock)
}

func (a *archive) knownAbsent(runID string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	until, ok := a.absent[runID]
	return ok && now.Before(until)
}

func (a *archive) noteAbsent(runID string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.absent) >= maxAbsentRuns {
		clear(a.absent)
	}
	a.absent[runID] = now.Add(absentTTL)
}

func (a *archive) forgetAbsent(runID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.absent, runID)
}

// safety: a failing store stops the pass at its first error and the next
// pass waits out a growing, capped delay, so an outage costs a handful of
// requests rather than one per idle run per minute. The logs stay on the
// volume meanwhile.
func (a *archive) ready(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !now.Before(a.retryAt)
}

func (a *archive) failed(now time.Time) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures++
	a.retryAt = now.Add(minArchiveBackoff + a.backoff.Delay(a.failures-1))
	return a.retryAt
}

func (a *archive) succeeded() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures = 0
	a.retryAt = time.Time{}
}

func indexRunRel(runID string) string { return indexRunsRel + runID + ".json" }

func runObjectRel(runID, rel string) string { return "runs/" + runID + "/" + rel }

func readRunMeta(root *os.Root, runID string) runMeta {
	var m runMeta
	data, err := readLogFile(root, filepath.Join(runID, runMetaFile))
	if err != nil {
		return m
	}
	// safety: a torn metadata file records no team, which the caller treats
	// as the operator's run; it never names another team.
	if err := json.Unmarshal(data, &m); err != nil {
		return runMeta{}
	}
	return m
}

func (s *Server) writeRunMeta(root *os.Root, runID string, m runMeta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := root.OpenFile(filepath.Join(runID, runMetaFile), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, s.fileMode)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	return errors.Join(werr, f.Close())
}

// withRun holds the run on the volume for the life of the request,
// restoring it from the archive first when it is not there, and refuses a
// caller of one team a run recorded as another's. The controller has
// already answered whether the caller may read the run; the recorded team
// is the second, independent check that keeps one team's objects out of
// another's reach even if that answer were wrong.
func (s *Server) withRun(runID func(*http.Request) string, next http.Handler) http.Handler {
	if s.archive == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := runID(r)
		if id == "" || validateID(id) != nil {
			next.ServeHTTP(w, r)
			return
		}
		l := s.archive.lock(id)
		l.rw.RLock()
		defer l.rw.RUnlock()
		team, err := s.ensureLocal(r.Context(), id, l)
		if err != nil {
			s.logger.Error("logs archive", "op", "restore run", "run", id, "err", err)
			http.Error(w, "log archive unavailable", http.StatusBadGateway)
			return
		}
		if !s.teamMayUse(r, team) {
			http.Error(w, fmt.Sprintf("run %s not found", id), http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// principalTeam is the team a non-admin caller acts for, or "" when the
// caller is the operator, unauthenticated, or names no team.
func principalTeam(r *http.Request) string {
	p, ok := logsPrincipalFromContext(r.Context())
	if !ok || p == nil || p.hasScope(scopeAdmin) {
		return ""
	}
	return p.Team
}

func (s *Server) teamMayUse(r *http.Request, runTeam string) bool {
	caller := principalTeam(r)
	return caller == "" || runTeam == "" || caller == runTeam
}

// ensureLocal returns the run's recorded team, restoring the run from the
// archive when the volume does not hold it. The caller holds l.rw shared.
func (s *Server) ensureLocal(ctx context.Context, runID string, l *runLock) (string, error) {
	root, err := s.openRunsRoot()
	if err != nil {
		return "", err
	}
	defer s.closeRoot(root, "ensure local run")
	if _, err := root.Stat(runID); err == nil {
		return readRunMeta(root, runID).Team, nil
	}
	now := time.Now()
	if s.archive.knownAbsent(runID, now) {
		return "", nil
	}
	l.hydrate.Lock()
	defer l.hydrate.Unlock()
	if _, err := root.Stat(runID); err == nil {
		return readRunMeta(root, runID).Team, nil
	}
	idx, err := s.readRunIndex(ctx, runID)
	if errors.Is(err, teamblob.ErrNotFound) {
		s.archive.noteAbsent(runID, now)
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return idx.Team, s.restoreRun(ctx, root, runID, idx)
}

func (s *Server) readRunIndex(ctx context.Context, runID string) (runIndex, error) {
	var idx runIndex
	data, err := s.archive.store.ReadAll(ctx, "", indexRunRel(runID))
	if err != nil {
		return idx, err
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return idx, fmt.Errorf("index for run %s: %w", runID, err)
	}
	if idx.Team != "" && !teamblob.ValidTeam(idx.Team) {
		return idx, fmt.Errorf("index for run %s names %q, which is not a team", runID, idx.Team)
	}
	return idx, nil
}

// restoreRun downloads the run beside the runs tree and renames it into
// place, so no reader ever sees half a run.
func (s *Server) restoreRun(ctx context.Context, root *os.Root, runID string, idx runIndex) error {
	stage := filepath.Join(s.root, rehydrateDir)
	if err := s.ensureDir(stage); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(stage, "run-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(tmp); err != nil {
			s.logger.Error("logs archive", "op", "remove restore stage", "err", err)
		}
	}()
	stageRoot, err := os.OpenRoot(tmp)
	if err != nil {
		return err
	}
	defer s.closeRoot(stageRoot, "restore run")
	var kept []archivedFile
	for _, f := range idx.Files {
		if !safeArchivedRel(f.Rel) {
			return fmt.Errorf("index for run %s names %q, which is not a log file", runID, f.Rel)
		}
		data, err := s.archive.store.ReadAll(ctx, idx.Team, runObjectRel(runID, f.Rel))
		if errors.Is(err, teamblob.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		local := filepath.FromSlash(f.Rel)
		if dir := filepath.Dir(local); dir != "." {
			if err := stageRoot.MkdirAll(dir, s.dirMode); err != nil {
				return err
			}
		}
		if err := stageRoot.WriteFile(local, data, s.fileMode); err != nil {
			return err
		}
		kept = append(kept, archivedFile{Rel: f.Rel, Size: int64(len(data))})
	}
	meta := runMeta{Team: idx.Team, ArchivedAt: time.Now(), Files: kept}
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := stageRoot.WriteFile(runMetaFile, data, s.fileMode); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(s.root, "runs", runID)); err != nil {
		// safety: a restore racing another for the same run loses to it, and
		// the run it lost to holds the same files.
		if _, serr := root.Stat(runID); serr == nil {
			return nil
		}
		return err
	}
	return nil
}

// safety: an index names files by path, so one that climbs out of the run
// or names the metadata file is refused rather than written.
func safeArchivedRel(rel string) bool {
	if rel == "" || rel == runMetaFile || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return false
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return strings.HasSuffix(rel, ".log")
}

// labelRun records the appending caller's team on a run the first time a
// team's credential writes to it, after the controller has validated the
// write's claim. It refuses a write into a run recorded for another team.
func (s *Server) labelRun(r *http.Request, root *os.Root, runID string) error {
	if s.archive == nil {
		return nil
	}
	team := principalTeam(r)
	meta := readRunMeta(root, runID)
	switch {
	case team == "" || meta.Team == team:
		return nil
	case meta.Team != "":
		return errRunOfAnotherTeam
	}
	if !teamblob.ValidTeam(team) {
		return nil
	}
	meta.Team = team
	return s.writeRunMeta(root, runID, meta)
}

var errRunOfAnotherTeam = errors.New("this run belongs to another team")

type localFile struct {
	rel     string
	size    int64
	modTime time.Time
}

func runFiles(root *os.Root, runID string) ([]localFile, error) {
	var out []localFile
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		entries, err := readDirAt(root, dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			childRel := name
			if rel != "" {
				childRel = rel + "/" + name
			}
			if e.IsDir() {
				if err := walk(filepath.Join(dir, name), childRel); err != nil {
					return err
				}
				continue
			}
			if !safeArchivedRel(childRel) {
				continue
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			out = append(out, localFile{rel: childRel, size: info.Size(), modTime: info.ModTime()})
		}
		return nil
	}
	return out, walk(runID, "")
}

// ArchiveOnce uploads every run idle past ArchiveOptions.Idle and removes
// it from the volume. A run in use by a request is left for the next pass.
// It stops at the first failed upload and waits a growing delay before
// trying again, leaving every run it did not finish on the volume.
func (s *Server) ArchiveOnce(ctx context.Context, now time.Time) (int, error) {
	a := s.archive
	if a == nil || !a.ready(now) {
		return 0, nil
	}
	root, err := s.openRunsRoot()
	if err != nil {
		return 0, err
	}
	defer s.closeRoot(root, "archive")
	d, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return 0, err
	}
	archived := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		if !e.IsDir() || validateID(e.Name()) != nil {
			continue
		}
		runID := e.Name()
		if _, newest := logTreeUsage(root, runID); now.Sub(newest) < a.opts.Idle {
			continue
		}
		l := a.lock(runID)
		if !l.rw.TryLock() {
			continue
		}
		err := s.archiveRun(ctx, root, runID)
		if err == nil {
			err = root.RemoveAll(runID)
			s.runTotals.forget(runID)
			a.forgetAbsent(runID)
			a.locks.Delete(runID)
		}
		l.rw.Unlock()
		if err != nil {
			retry := a.failed(now)
			return archived, fmt.Errorf("archive run %s (next attempt after %s): %w", runID, retry.UTC().Format(time.RFC3339), err)
		}
		archived++
	}
	a.succeeded()
	return archived, nil
}

func (s *Server) archiveRun(ctx context.Context, root *os.Root, runID string) error {
	meta := readRunMeta(root, runID)
	files, err := runFiles(root, runID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	store := s.archive.store
	committed := meta.committedSizes()
	if meta.Uploaded == nil {
		meta.Uploaded = map[string]int64{}
	}
	sizes := maps.Clone(committed)
	var lastWrite time.Time
	changed := false
	for _, f := range files {
		lastWrite = maxTime(lastWrite, f.modTime)
		sizes[f.rel] = f.size
		// safety: an append only ever grows a file, so a size that matches
		// what the store holds means the object is current, whatever the
		// coarse file clock says.
		if prior, ok := committed[f.rel]; ok && prior == f.size {
			continue
		}
		changed = true
		// perf: a retry after a later PUT failed resends nothing that
		// already landed, so it costs one request, and the breaker sees an
		// unbroken run of failures instead of successes resetting it.
		if meta.Uploaded[f.rel] == f.size {
			continue
		}
		_, known := committed[f.rel]
		_, tried := meta.Uploaded[f.rel]
		fh, err := root.Open(filepath.Join(runID, filepath.FromSlash(f.rel)))
		if err != nil {
			return err
		}
		_, err = store.Put(ctx, meta.Team, runObjectRel(runID, f.rel), fh, teamblob.PutOptions{
			Size:        f.size,
			ContentType: "text/plain; charset=utf-8",
			Fresh:       !known && !tried,
		})
		_ = fh.Close()
		if err != nil {
			return err
		}
		meta.Uploaded[f.rel] = f.size
		if err := s.writeRunMeta(root, runID, meta); err != nil {
			return err
		}
	}
	if !changed {
		return nil
	}
	idx := runIndex{Team: meta.Team, LastWrite: lastWrite.UTC()}
	for rel, size := range sizes {
		idx.Files = append(idx.Files, archivedFile{Rel: rel, Size: size})
	}
	slices.SortFunc(idx.Files, func(a, b archivedFile) int { return strings.Compare(a.Rel, b.Rel) })
	body, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	if meta.IndexDigest != digest {
		if _, err := store.Put(ctx, "", indexRunRel(runID), bytes.NewReader(body), teamblob.PutOptions{
			Size: int64(len(body)), ContentType: "application/json",
		}); err != nil {
			return err
		}
		meta.IndexDigest = digest
		if err := s.writeRunMeta(root, runID, meta); err != nil {
			return err
		}
	}
	day := indexDaysRel + lastWrite.UTC().Format(dayLayout) + "/" + runID
	_, err = store.Put(ctx, "", day, bytes.NewReader(nil), teamblob.PutOptions{Size: 0})
	return err
}

// committedSizes is what the store holds for the run as of its last
// finished archive.
func (m runMeta) committedSizes() map[string]int64 {
	sizes := map[string]int64{}
	for _, f := range m.Files {
		sizes[f.Rel] = f.Size
	}
	return sizes
}

// writtenSinceArchive reports whether any local file differs from what
// the last finished archive stored. A restore rewrites every file, so the
// file clock cannot tell a restored copy from a written one; the sizes can,
// because an append only ever grows a file.
func writtenSinceArchive(meta runMeta, files []localFile) bool {
	committed := meta.committedSizes()
	for _, f := range files {
		if prior, ok := committed[f.rel]; !ok || prior != f.size {
			return true
		}
	}
	return false
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// deleteArchivedRun removes a run's objects and its index. The caller
// holds the run exclusively. The day index entry is left for retention,
// which drops an entry whose run is gone.
func (s *Server) deleteArchivedRun(ctx context.Context, runID string) error {
	idx, err := s.readRunIndex(ctx, runID)
	if errors.Is(err, teamblob.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	objs := make([]teamblob.Sized, 0, len(idx.Files))
	for _, f := range idx.Files {
		if safeArchivedRel(f.Rel) {
			objs = append(objs, teamblob.Sized{Rel: runObjectRel(runID, f.Rel), Size: f.Size})
		}
	}
	if err := s.archive.store.DeleteMany(ctx, idx.Team, objs); err != nil {
		return err
	}
	return s.archive.store.Delete(ctx, "", indexRunRel(runID))
}

// PruneArchive deletes archived runs whose last write is older than the
// retention. It lists the day index once and, for each day wholly past
// the cutoff, that day's entries once; each expired run then costs one
// read of its index, and the deletes go out a thousand keys per request.
// It never lists a run's objects.
func (s *Server) PruneArchive(ctx context.Context, now time.Time) (int, error) {
	a := s.archive
	if a == nil || s.limits.Retention <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-s.limits.Retention)
	days, err := a.store.ListDirs(ctx, "", indexDaysRel)
	if err != nil {
		return 0, err
	}
	slices.Sort(days)
	pruned := 0
	for _, day := range days {
		start, perr := time.Parse(dayLayout, day)
		if perr != nil {
			continue
		}
		if start.Add(24 * time.Hour).After(cutoff) {
			break
		}
		n, err := s.pruneDay(ctx, day, cutoff)
		pruned += n
		if err != nil {
			return pruned, err
		}
	}
	if pruned > 0 && s.ceiling.Enforced() {
		if err := s.MeasureStore(ctx); err != nil {
			s.logger.Error("logs store", "op", "measure store", "err", err)
		}
	}
	return pruned, nil
}

func (s *Server) pruneDay(ctx context.Context, day string, cutoff time.Time) (int, error) {
	store := s.archive.store
	dayRel := indexDaysRel + day + "/"
	entries, err := store.List(ctx, "", dayRel)
	if err != nil {
		return 0, err
	}
	root, err := s.openRunsRoot()
	if err != nil {
		return 0, err
	}
	defer s.closeRoot(root, "prune archive")
	// safety: an expired run is held exclusively from the decision to its
	// deletion, so no request restores it or writes to it in between.
	var held, expired []string
	defer func() {
		for _, runID := range held {
			s.archive.lock(runID).rw.Unlock()
		}
	}()
	byTeam := map[string][]teamblob.Sized{}
	var operator []teamblob.Sized
	for _, e := range entries {
		runID := path.Base(e.Rel)
		entry := teamblob.Sized{Rel: e.Rel, Size: e.Size}
		if validateID(runID) != nil {
			operator = append(operator, entry)
			continue
		}
		body, err := store.ReadAll(ctx, "", indexRunRel(runID))
		if errors.Is(err, teamblob.ErrNotFound) {
			operator = append(operator, entry)
			continue
		}
		if err != nil {
			return 0, err
		}
		var idx runIndex
		if err := json.Unmarshal(body, &idx); err != nil || (idx.Team != "" && !teamblob.ValidTeam(idx.Team)) {
			operator = append(operator, entry)
			continue
		}
		// The run was written again after this day; its newer day entry
		// carries it, and this one only goes.
		if idx.LastWrite.After(cutoff) {
			operator = append(operator, entry)
			continue
		}
		// A run in use, or restored and written since its archive, is not
		// expired: its latest write is on the volume, and the archiver
		// uploads it with a new day entry. This entry stays so the run is
		// judged again if that never happens.
		l := s.archive.lock(runID)
		if !l.rw.TryLock() {
			continue
		}
		held = append(held, runID)
		if written, err := s.localWrittenSinceArchive(root, runID); err != nil || written {
			if err != nil {
				s.logger.Error("logs archive", "op", "prune read local copy", "run", runID, "err", err)
			}
			continue
		}
		for _, f := range idx.Files {
			if safeArchivedRel(f.Rel) {
				byTeam[idx.Team] = append(byTeam[idx.Team], teamblob.Sized{Rel: runObjectRel(runID, f.Rel), Size: f.Size})
			}
		}
		operator = append(operator, entry, teamblob.Sized{Rel: indexRunRel(runID), Size: int64(len(body))})
		expired = append(expired, runID)
	}
	for team, objs := range byTeam {
		if err := store.DeleteMany(ctx, team, objs); err != nil {
			return 0, err
		}
	}
	for _, runID := range expired {
		if err := root.RemoveAll(runID); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.logger.Error("logs archive", "op", "prune local copy", "run", runID, "err", err)
		}
		s.runTotals.forget(runID)
	}
	return len(expired), store.DeleteMany(ctx, "", operator)
}

// localWrittenSinceArchive reports whether the volume holds a copy of the
// run that was written after its last archive.
func (s *Server) localWrittenSinceArchive(root *os.Root, runID string) (bool, error) {
	if _, err := root.Stat(runID); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	files, err := runFiles(root, runID)
	if err != nil {
		return false, err
	}
	return writtenSinceArchive(readRunMeta(root, runID), files), nil
}

// startArchive runs the archiver, retention over the archive, and the
// per-team count's upkeep for the life of ctx.
func (s *Server) startArchive(ctx context.Context) {
	a := s.archive
	if a == nil {
		return
	}
	go a.store.Maintain(ctx, a.opts.UsageReconcile, 5*time.Minute, func(op string, err error) {
		s.logger.Error("logs archive", "op", op, "err", err)
	})
	if err := storagequota.RegisterMetric(otelutil.Meter("sparkwing-logs"), "logs", a.quota); err != nil {
		s.logger.Error("logs archive", "op", "register free storage metric", "err", err)
	}
	go func() {
		if err := s.MeasureVolumeUsage(ctx); err != nil {
			s.logger.Error("logs archive", "op", "measure volume usage", "err", err)
		}
		t := time.NewTicker(a.opts.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if n, err := s.ArchiveOnce(ctx, now); err != nil {
					s.logger.Error("logs archive", "op", "archive", "archived", n, "err", err)
				}
				if err := s.MeasureVolumeUsage(ctx); err != nil {
					s.logger.Error("logs archive", "op", "measure volume usage", "err", err)
				}
				a.mu.Lock()
				due := now.Sub(a.lastPrune) >= a.opts.PruneInterval
				if due {
					a.lastPrune = now
				}
				a.mu.Unlock()
				if due {
					if n, err := s.PruneArchive(ctx, now); err != nil {
						s.logger.Error("logs archive", "op", "prune", "pruned", n, "err", err)
					}
				}
			}
		}
	}()
}

// handleDeleteTeamLogs removes every log a team holds, in the archive and
// on the volume, for the controller deleting that team. It is idempotent,
// so a deletion that stopped part-way is finished by sending it again.
func (s *Server) handleDeleteTeamLogs(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	if !teamblob.ValidTeam(team) {
		writeLogsErr(w, http.StatusBadRequest, "not a team slug")
		return
	}
	if s.archive == nil {
		writeLogsErr(w, http.StatusNotFound, "this logs service keeps no per-team store; start it with --archive-store, or delete the team's runs one at a time")
		return
	}
	root, err := s.openRunsRoot()
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	defer s.closeRoot(root, "delete team logs")
	d, err := root.Open(".")
	if err != nil {
		s.storeError(w, "open runs root", err)
		return
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		s.storeError(w, "read runs root", err)
		return
	}
	for _, e := range entries {
		if !e.IsDir() || readRunMeta(root, e.Name()).Team != team {
			continue
		}
		l := s.archive.lock(e.Name())
		l.rw.Lock()
		err := root.RemoveAll(e.Name())
		l.rw.Unlock()
		if err != nil {
			s.storeError(w, "remove run dir", err)
			return
		}
		s.runTotals.forget(e.Name())
	}
	// Indexes go first: once the team's objects are gone, nothing names the
	// runs whose indexes a retried purge would still have to find.
	if err := s.deleteTeamIndexes(r.Context(), team); err != nil {
		s.logger.Error("logs archive", "op", "delete team indexes", "team", team, "err", err)
		writeLogsErr(w, http.StatusBadGateway, "delete the team's archived logs: the object store refused; retry")
		return
	}
	deleted, err := s.archive.store.DeleteTeam(r.Context(), team)
	if err != nil {
		s.logger.Error("logs archive", "op", "delete team", "team", team, "err", err)
		writeLogsErr(w, http.StatusBadGateway, "delete the team's archived logs: the object store refused; retry")
		return
	}
	//nolint:contextcheck // the walk must outlive the delete that triggered it; the sweeper's context bounds it.
	s.remeasureAfterDelete()
	writeJSONResponse(w, http.StatusOK, deleted)
}

// deleteTeamIndexes removes the run and day index entries of every run
// team has archived. It lists the team's runs, one delimited LIST per
// thousand, and reads each run's index to confirm the team and find its
// day entry; an older day entry of a run archived more than once is left
// for retention, which drops an entry whose run is gone.
func (s *Server) deleteTeamIndexes(ctx context.Context, team string) error {
	store := s.archive.store
	runs, err := store.ListDirs(ctx, team, "runs/")
	if err != nil {
		return err
	}
	var operator []teamblob.Sized
	for _, runID := range runs {
		if validateID(runID) != nil {
			continue
		}
		body, err := store.ReadAll(ctx, "", indexRunRel(runID))
		if errors.Is(err, teamblob.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		// safety: an index this purge cannot read as the team's is left
		// alone; retention or a run delete owns it.
		var idx runIndex
		if json.Unmarshal(body, &idx) != nil || idx.Team != team {
			continue
		}
		operator = append(operator,
			teamblob.Sized{Rel: indexDaysRel + idx.LastWrite.UTC().Format(dayLayout) + "/" + runID},
			teamblob.Sized{Rel: indexRunRel(runID), Size: int64(len(body))},
		)
	}
	return store.DeleteMany(ctx, "", operator)
}

// TeamLogsUsage is what one team's archived logs hold.
type TeamLogsUsage struct {
	Team         string `json:"team"`
	Bytes        int64  `json:"bytes"`
	Objects      int64  `json:"objects"`
	ReconciledAt string `json:"reconciled_at,omitempty"`
}

// handleTeamLogsUsage reports the running count of a team's archived logs,
// which the controller's storage allowance reads. It sends no request to
// the object store.
func (s *Server) handleTeamLogsUsage(w http.ResponseWriter, r *http.Request) {
	team := r.PathValue("team")
	if !teamblob.ValidTeam(team) {
		writeLogsErr(w, http.StatusBadRequest, "not a team slug")
		return
	}
	if s.archive == nil {
		writeLogsErr(w, http.StatusNotFound, "this logs service keeps no per-team count; start it with --archive-store")
		return
	}
	u := s.archive.store.Usage().Team(team)
	out := TeamLogsUsage{Team: team, Bytes: u.Bytes, Objects: u.Objects}
	if !u.ReconciledAt.IsZero() {
		out.ReconciledAt = u.ReconciledAt.UTC().Format(time.RFC3339)
	}
	writeJSONResponse(w, http.StatusOK, out)
}

// mayDeleteArchivedRun deletes the run's archived objects after checking
// the caller's team against the run's recorded one, and answers the
// request itself when it must stop. The caller holds the run exclusively.
func (s *Server) mayDeleteArchivedRun(w http.ResponseWriter, r *http.Request, root *os.Root, runID string) bool {
	team := readRunMeta(root, runID).Team
	if _, err := root.Stat(runID); err != nil {
		idx, err := s.readRunIndex(r.Context(), runID)
		switch {
		case errors.Is(err, teamblob.ErrNotFound):
		case err != nil:
			s.logger.Error("logs archive", "op", "read run index", "run", runID, "err", err)
			http.Error(w, "log archive unavailable", http.StatusBadGateway)
			return false
		default:
			team = idx.Team
		}
	}
	if !s.teamMayUse(r, team) {
		http.Error(w, fmt.Sprintf("run %s not found", runID), http.StatusNotFound)
		return false
	}
	if err := s.deleteArchivedRun(r.Context(), runID); err != nil {
		s.logger.Error("logs archive", "op", "delete run", "run", runID, "err", err)
		http.Error(w, "delete the run's archived logs: the object store refused; retry", http.StatusBadGateway)
		return false
	}
	s.archive.noteAbsent(runID, time.Now())
	return true
}

// safety: the health route answers without a token, so it names the
// archive's state and never a bucket, key or team.
func (s *Server) archiveHealth() (map[string]any, []string) {
	a := s.archive
	if a == nil {
		return map[string]any{"enabled": false}, nil
	}
	a.mu.Lock()
	failures, retryAt := a.failures, a.retryAt
	a.mu.Unlock()
	breaker := a.store.Breaker()
	state := map[string]any{"enabled": true, "failed_passes": failures, "writes_paused": breaker.Open}
	var problems []string
	if failures > 0 {
		state["retry_at"] = retryAt.UTC().Format(time.RFC3339)
		problems = append(problems, "archive: uploads to the object store are failing, so finished runs stay on the volume")
	}
	if breaker.Open {
		state["paused_until"] = breaker.Until.UTC().Format(time.RFC3339)
		problems = append(problems, "archive: the object store refused writes and they are paused")
	}
	return state, problems
}
