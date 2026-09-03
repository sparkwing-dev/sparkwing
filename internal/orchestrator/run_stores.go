package orchestrator

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

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
	open  []openStandalone
	notes []string
}

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
	needed := map[string]int{}
	var order []string
	for _, entry := range paths.StandaloneStores() {
		label := standaloneLabel(paths, entry.Path)
		st, err := store.OpenReadOnly(entry.Path)
		if err != nil {
			s.notes = append(s.notes, fmt.Sprintf("standalone store %s cannot be opened: %v", label, err))
			continue
		}
		skew, err := st.RequirementSkew(ctx)
		if err != nil {
			_ = st.Close()
			s.notes = append(s.notes, fmt.Sprintf("standalone store %s cannot be read: %v", label, err))
			continue
		}
		if skew != nil {
			_ = st.Close()
			if _, seen := needed[skew.MinVersion]; !seen {
				order = append(order, skew.MinVersion)
			}
			needed[skew.MinVersion]++
			continue
		}
		if note, ok := unreadableNote(ctx, st, label); ok {
			_ = st.Close()
			s.notes = append(s.notes, note)
			continue
		}
		s.open = append(s.open, openStandalone{label: label, path: entry.Path, st: st})
	}
	for _, version := range order {
		s.notes = append(s.notes, skewNote(needed[version], version))
	}
	return s
}

// safety: the requirements rule governs whether a store may be opened, not
// whether this build's SELECT matches its columns, so a store at an older
// store schema passes it and still lacks a column a later migration added.
func unreadableNote(ctx context.Context, st *store.Store, label string) (string, bool) {
	if _, err := st.ListRuns(ctx, store.RunFilter{Limit: 1}); err == nil {
		return "", false
	}
	runs := standaloneRunCount(ctx, st)
	if runs < 0 {
		return fmt.Sprintf("standalone store %s cannot be read by this sparkwing", label), true
	}
	return fmt.Sprintf(
		"%s holds %s written by an older sparkwing; read them with that release",
		label, pluralRuns(runs)), true
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

func skewNote(count int, minVersion string) string {
	subject := "standalone stores need"
	if count == 1 {
		subject = "standalone store needs"
	}
	if minVersion == "" || minVersion == "(devel)" {
		return fmt.Sprintf("%d %s a newer sparkwing", count, subject)
	}
	return fmt.Sprintf("%d %s sparkwing >= %s", count, subject, minVersion)
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
			s.notes = append(s.notes, fmt.Sprintf("standalone store %s cannot be read by this sparkwing", o.label))
			continue
		}
		for _, r := range runs {
			out = append(out, TaggedRun{Run: r, Store: o.label})
		}
	}
	return out
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
// it is one of them, so a merged listing agrees with the single-id verbs,
// which resolve the shared store first.
func MergeTaggedRuns(rows []TaggedRun) []TaggedRun {
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].StartedAt.Equal(rows[j].StartedAt) {
			return rows[i].StartedAt.After(rows[j].StartedAt)
		}
		if rows[i].ID != rows[j].ID {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].Store == SharedStoreLabel && rows[j].Store != SharedStoreLabel
	})
	seen := make(map[string]bool, len(rows))
	out := rows[:0:0]
	for _, r := range rows {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
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
	spec, _, _ := profileSurfaceSpecs(p, paths.StateDB())
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
	_ = standalone.Close()
	return shared, SharedStoreLabel, closeShared, nil
}

// OpenStoreForRunWrite opens the store holding runID for writing, so a verb
// that changes a run changes it where it lives. The shared store answers first;
// a run found only in a standalone store is written in that store.
//
// A standalone store is opened read-write only when catching it up to this
// binary's schema would stamp no requirement it does not already list, because
// a requirement it lacks is what puts the file out of reach of the pipeline
// binary that owns it. Otherwise the returned error names that requirement.
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
	held, label, _, ok := standalone.Find(ctx, runID)
	if !ok {
		_ = standalone.Close()
		return shared, SharedStoreLabel, closeShared, nil
	}
	path := standalonePathFor(standalone, label)
	adds, aerr := held.RequirementsWritingWouldAdd(ctx)
	_ = standalone.Close()
	closeShared()
	if aerr != nil {
		return nil, "", nil, fmt.Errorf("%s: standalone store %s cannot be read: %w", verb, path, aerr)
	}
	if len(adds) > 0 {
		return nil, "", nil, fmt.Errorf(
			"%s: run %s lives in the standalone store %s, and writing to it would add the schema %s, "+
				"which would put the file out of reach of the pipeline binary that wrote it; "+
				"leave it to that binary, or delete the store when its runs no longer matter",
			verb, runID, path, requirementPhrase(adds))
	}
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

func requirementPhrase(names []string) string {
	if len(names) == 1 {
		return "requirement " + names[0]
	}
	return "requirements " + strings.Join(names, ", ")
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
