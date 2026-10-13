package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// SharedStoreLabel names a home's own runs store in the store field the read
// verbs tag every row with. A run that went standalone carries its store's
// path relative to the home instead.
const SharedStoreLabel = "shared"

// TaggedRun is a run together with the store it was read from:
// [SharedStoreLabel], or a standalone store's path relative to the home.
type TaggedRun struct {
	*store.Run
	Store string `json:"store"`
}

// StandaloneStores holds every standalone runs store under one home, each
// opened read-only. A store this binary did not read is absent from the set
// and named in [StandaloneStores.Notes].
type StandaloneStores struct {
	open   []openStandalone
	notes  []string
	failed bool
}

func (s *StandaloneStores) noteOnce(note string) {
	if slices.Contains(s.notes, note) {
		return
	}
	s.notes = append(s.notes, note)
}

// Failed reports whether a store stopped answering mid-read, so a caller can say its
// result is short rather than whole.
func (s *StandaloneStores) Failed() bool { return s != nil && s.failed }

type openStandalone struct {
	label string
	path  string
	st    *store.Store
}

// OpenStandaloneStores opens the standalone runs stores under paths read-only,
// which is what keeps a verb that reports on a store from migrating it. A home
// with no standalone directory yields an empty set. The caller closes the
// result.
//
// A store this binary's queries cannot read is left out of the set and named
// in [StandaloneStores.Notes] with its run count, so it is reported rather
// than silently dropped or surfaced as driver text.
func OpenStandaloneStores(ctx context.Context, paths Paths) *StandaloneStores {
	s := &StandaloneStores{}
	for _, entry := range paths.StandaloneStores() {
		label := standaloneLabel(paths, entry.Path)
		st, err := store.OpenReadOnly(entry.Path)
		if err != nil {
			s.notes = append(s.notes, unopenableNote(label, err))
			continue
		}
		if note, ok := unreadableNote(ctx, st, label); ok {
			_ = st.Close()
			s.notes = append(s.notes, note)
			continue
		}
		s.open = append(s.open, openStandalone{label: label, path: entry.Path, st: st})
	}
	return s
}

// safety: a read-only open checks no schema version, so a store from another
// baseline opens and then fails this build's SELECT.
func unreadableNote(ctx context.Context, st *store.Store, label string) (string, bool) {
	_, err := st.ListRuns(ctx, store.RunFilter{Limit: 1})
	if err == nil {
		return "", false
	}
	if store.IsBusyErr(err) || store.IsProtocolErr(err) {
		return fmt.Sprintf("standalone store %s is busy; it was skipped this time", label), true
	}
	runs := standaloneRunCount(ctx, st)
	if runs < 0 {
		return fmt.Sprintf("standalone store %s cannot be read by this sparkwing", label), true
	}
	return fmt.Sprintf(
		"%s holds %s written by an older sparkwing; read them with that release",
		label, pluralRuns(runs)), true
}

func unopenableNote(label string, err error) string {
	if store.IsBusyErr(err) || store.IsProtocolErr(err) {
		return fmt.Sprintf("standalone store %s is busy; it was skipped this time", label)
	}
	return fmt.Sprintf("standalone store %s is not a runs store this sparkwing can read", label)
}

func standaloneRunCount(ctx context.Context, st *store.Store) int {
	var runs int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		return -1
	}
	return runs
}

func pluralRuns(n int) string {
	if n == 1 {
		return "1 run"
	}
	return fmt.Sprintf("%d runs", n)
}

func standaloneLabel(paths Paths, path string) string {
	rel, err := filepath.Rel(paths.Root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

// Close releases every store handle the set holds.
func (s *StandaloneStores) Close() error {
	if s == nil {
		return nil
	}
	var first error
	for _, o := range s.open {
		if err := o.st.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.open = nil
	return first
}

// Notes returns one line for each standalone store this binary did not read,
// naming the release that can read it when age is the reason.
func (s *StandaloneStores) Notes() []string {
	if s == nil {
		return nil
	}
	return s.notes
}

// Empty reports whether the home holds no readable standalone store.
func (s *StandaloneStores) Empty() bool { return s == nil || len(s.open) == 0 }

// ListRuns returns the runs matching filter from every readable standalone
// store, each tagged with the store it came from.
func (s *StandaloneStores) ListRuns(ctx context.Context, filter store.RunFilter) []TaggedRun {
	if s == nil {
		return nil
	}
	var out []TaggedRun
	for _, o := range s.open {
		runs, err := o.st.ListRuns(ctx, filter)
		if err != nil {
			s.noteOnce(fmt.Sprintf("standalone store %s cannot be read by this sparkwing", o.label))
			s.failed = true
			continue
		}
		for _, r := range runs {
			out = append(out, TaggedRun{Run: r, Store: o.label})
		}
	}
	return out
}

// Contributed reports whether any standalone store bears on a listing's count: one
// holding a matching run, or one this binary could not read. An open store matching
// nothing leaves the shared store's count exact.
func (s *StandaloneStores) Contributed(ctx context.Context, filter store.RunFilter) bool {
	if s == nil {
		return false
	}
	if len(s.notes) > 0 {
		return true
	}
	counted := filter
	counted.Limit = 0
	counted.AfterStartedAt, counted.AfterID = 0, ""
	for _, o := range s.open {
		n, err := o.st.CountRuns(ctx, counted)
		if err != nil || n > 0 {
			return true
		}
	}
	return false
}

// Find returns the first standalone store holding runID together with its
// label and run row.
func (s *StandaloneStores) Find(ctx context.Context, runID string) (*store.Store, string, *store.Run, bool) {
	if s == nil {
		return nil, "", nil, false
	}
	for _, o := range s.open {
		run, err := o.st.GetRun(ctx, runID)
		if err == nil && run != nil {
			return o.st, o.label, run, true
		}
	}
	return nil, "", nil, false
}

// StoreFor returns the standalone store carrying label, which is the label a
// row was tagged with, so a caller reading more of that row reads the store it
// was listed from rather than the first store that answers to its id.
func (s *StandaloneStores) StoreFor(label string) (*store.Store, bool) {
	if s == nil {
		return nil, false
	}
	for _, o := range s.open {
		if o.label == label {
			return o.st, true
		}
	}
	return nil, false
}

// WriteStandaloneNotes prints the notes a read verb collected, one per line.
func WriteStandaloneNotes(w io.Writer, notes []string) {
	for _, n := range notes {
		fmt.Fprintf(w, "sparkwing: %s\n", n)
	}
}

// TagShared labels runs read from a home's own store, or from the controller
// a profile names, which is the same store from the operator's side.
func TagShared(runs []*store.Run) []TaggedRun {
	out := make([]TaggedRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, TaggedRun{Run: r, Store: SharedStoreLabel})
	}
	return out
}

// MergeTaggedRuns orders runs from several stores newest first, breaking a tie
// on run id so two stores that stamped the same instant still list stably.
//
// An id present in more than one store lists once, from the shared store when
// it is one of them, whatever the copies say about when they started, so a
// merged listing agrees with the single-id verbs, which resolve the shared
// store first.
func MergeTaggedRuns(rows []TaggedRun) []TaggedRun {
	at := make(map[string]int, len(rows))
	out := rows[:0:0]
	for _, r := range rows {
		i, seen := at[r.ID]
		if !seen {
			at[r.ID] = len(out)
			out = append(out, r)
			continue
		}
		if out[i].Store != SharedStoreLabel && r.Store == SharedStoreLabel {
			out[i] = r
		}
	}
	// safety: this order is the one the store returns and the cursor predicate assumes;
	// a walk resuming after the last merged row would otherwise land inside a tie group.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out
}

// safety: a profile may name a sqlite file that is not this home's store, and
// this home's standalone runs are not that database's runs.
func readsMachineStore(paths Paths, p *profile.Profile) bool {
	if p == nil {
		resolved, err := defaultProfile()
		if err != nil {
			return true
		}
		p = resolved
	}
	spec := profileStateSpec(p, paths.StateDB())
	return spec != nil && spec.Type == backends.TypeSQLite && spec.Path == paths.StateDB()
}

func mergesStandalone(b backend.Backend, paths Paths, p *profile.Profile) bool {
	return localStore(b) != nil && readsMachineStore(paths, p)
}

// OpenStoreForRun opens the store holding runID for reading: this home's own
// store when it has the run, otherwise the standalone store that does. The
// label is [SharedStoreLabel] or the standalone store's path relative to the
// home, and it is [SharedStoreLabel] when no store holds the run, so the caller
// reports the shared store's own not-found. The returned function releases
// every handle the lookup opened.
func OpenStoreForRun(ctx context.Context, paths Paths, runID string) (*store.Store, string, func(), error) {
	shared, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, "", nil, err
	}
	closeShared := func() { _ = shared.Close() }
	if run, gerr := shared.GetRun(ctx, runID); gerr == nil && run != nil {
		return shared, SharedStoreLabel, closeShared, nil
	}
	standalone := OpenStandaloneStores(ctx, paths)
	if st, label, _, ok := standalone.Find(ctx, runID); ok {
		closeShared()
		return st, label, func() { _ = standalone.Close() }, nil
	}
	WriteStandaloneNotes(os.Stderr, standalone.Notes())
	_ = standalone.Close()
	return shared, SharedStoreLabel, closeShared, nil
}

// OpenStoreForRunWrite opens the store holding runID for writing, so a verb
// that changes a run changes it where it lives. The shared store answers first;
// a run found only in a standalone store is written in that store.
//
// The label is [SharedStoreLabel] when no store holds the run, so the caller
// reports the shared store's own not-found.
func OpenStoreForRunWrite(
	ctx context.Context,
	paths Paths,
	runID, verb string,
) (*store.Store, string, func(), error) {
	shared, err := store.Open(paths.StateDB())
	if err != nil {
		return nil, "", nil, err
	}
	closeShared := func() { _ = shared.Close() }
	if run, gerr := shared.GetRun(ctx, runID); gerr == nil && run != nil {
		return shared, SharedStoreLabel, closeShared, nil
	}

	standalone := OpenStandaloneStores(ctx, paths)
	_, label, _, ok := standalone.Find(ctx, runID)
	if !ok {
		WriteStandaloneNotes(os.Stderr, standalone.Notes())
		_ = standalone.Close()
		return shared, SharedStoreLabel, closeShared, nil
	}
	path := standalonePathFor(standalone, label)
	_ = standalone.Close()
	closeShared()
	writable, werr := store.Open(path)
	if werr != nil {
		return nil, "", nil, fmt.Errorf("%s: standalone store %s: %w", verb, path, werr)
	}
	return writable, label, func() { _ = writable.Close() }, nil
}

func standalonePathFor(s *StandaloneStores, label string) string {
	for _, o := range s.open {
		if o.label == label {
			return o.path
		}
	}
	return label
}

// StandaloneCancelRefusal explains that a standalone run cannot be cancelled,
// naming the store it lives in and whatever the run recorded about the process
// still running it. It returns nil when no standalone store under paths holds
// runID.
//
// safety: no command is offered. A standalone run is by definition one no
// daemon arbitrates, so nothing is watching its store for a cancel request,
// and any command printed here would be one that cannot work.
func StandaloneCancelRefusal(ctx context.Context, paths Paths, runID, verb string) error {
	stores := OpenStandaloneStores(ctx, paths)
	defer func() { _ = stores.Close() }()
	held, label, run, ok := stores.Find(ctx, runID)
	if !ok {
		return nil
	}
	msg := fmt.Sprintf(
		"%s: run %s is standalone, in %s. No daemon arbitrates a standalone run, so nothing is watching "+
			"that store for a cancel request and sparkwing cannot cancel it",
		verb, runID, standalonePathFor(stores, label))
	if isTerminalStatus(run.Status) {
		return fmt.Errorf("%s; the run already finished (%s)", msg, run.Status)
	}
	if detail := runningNodeDetail(ctx, held, runID); detail != "" {
		return fmt.Errorf("%s; stop the process it reports (%s), or wait for it to finish", msg, detail)
	}
	return fmt.Errorf("%s; stop the process running it, or wait for it to finish", msg)
}

// StandaloneSubmitRefusal explains that a run in a standalone store cannot be
// retried, because a retry submits a new run and a standalone store has neither
// a daemon nor a controller to admit one. It returns nil when no standalone
// store under paths holds runID.
func StandaloneSubmitRefusal(ctx context.Context, paths Paths, runID, verb string) error {
	stores := OpenStandaloneStores(ctx, paths)
	defer func() { _ = stores.Close() }()
	_, label, _, ok := stores.Find(ctx, runID)
	if !ok {
		return nil
	}
	return fmt.Errorf(
		"%s: run %s is standalone, in %s. Retrying submits a new run, which needs the admission daemon "+
			"or a controller to admit it, and a standalone run has neither; start the pipeline again from "+
			"its repository instead",
		verb, runID, standalonePathFor(stores, label))
}

func runningNodeDetail(ctx context.Context, st *store.Store, runID string) string {
	nodes, err := st.ListNodes(ctx, runID)
	if err != nil {
		return ""
	}
	for _, n := range nodes {
		if n.FinishedAt == nil && n.StatusDetail != "" {
			return n.NodeID + ": " + n.StatusDetail
		}
	}
	return ""
}
