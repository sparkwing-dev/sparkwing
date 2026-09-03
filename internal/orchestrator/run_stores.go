package orchestrator

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

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
func OpenStandaloneStores(ctx context.Context, paths Paths) *StandaloneStores {
	s := &StandaloneStores{}
	needed := map[string]int{}
	var order []string
	for _, entry := range paths.StandaloneStores() {
		label := standaloneLabel(paths, entry.Path)
		st, err := store.OpenReadOnly(entry.Path)
		if err != nil {
			s.notes = append(s.notes, fmt.Sprintf("standalone store %s: %v", label, err))
			continue
		}
		skew, err := st.RequirementSkew(ctx)
		if err != nil {
			_ = st.Close()
			s.notes = append(s.notes, fmt.Sprintf("standalone store %s: %v", label, err))
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
		s.open = append(s.open, openStandalone{label: label, path: entry.Path, st: st})
	}
	for _, version := range order {
		s.notes = append(s.notes, skewNote(needed[version], version))
	}
	return s
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
// store, each tagged with the store it came from. A store that fails the query
// is skipped and named in [StandaloneStores.Notes].
func (s *StandaloneStores) ListRuns(ctx context.Context, filter store.RunFilter) []TaggedRun {
	if s == nil {
		return nil
	}
	var out []TaggedRun
	for _, o := range s.open {
		runs, err := o.st.ListRuns(ctx, filter)
		if err != nil {
			s.notes = append(s.notes, fmt.Sprintf("standalone store %s: %v", o.label, err))
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

// Locate names the standalone store holding runID: its label relative to the
// home, and the absolute path of the store file.
func (s *StandaloneStores) Locate(ctx context.Context, runID string) (label, path string, ok bool) {
	if s == nil {
		return "", "", false
	}
	for _, o := range s.open {
		run, err := o.st.GetRun(ctx, runID)
		if err == nil && run != nil {
			return o.label, o.path, true
		}
	}
	return "", "", false
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
func MergeTaggedRuns(rows []TaggedRun) []TaggedRun {
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].StartedAt.Equal(rows[j].StartedAt) {
			return rows[i].StartedAt.After(rows[j].StartedAt)
		}
		return rows[i].ID < rows[j].ID
	})
	return rows
}

// OpenStoreForRun opens the store holding runID: this home's own store when it
// has the run, otherwise the standalone store that does. The label is
// [SharedStoreLabel] or the standalone store's path relative to the home, and
// it is [SharedStoreLabel] when no store holds the run, so the caller reports
// the shared store's own not-found. The returned function releases every
// handle the lookup opened.
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

// StandaloneRunError describes a run that lives only in a standalone store,
// for a verb that writes and so needs the run's own store. It returns nil when
// no standalone store under paths holds runID.
func StandaloneRunError(ctx context.Context, paths Paths, runID, verb string) error {
	stores := OpenStandaloneStores(ctx, paths)
	defer func() { _ = stores.Close() }()
	_, path, ok := stores.Locate(ctx, runID)
	if !ok {
		return nil
	}
	remedy := strings.TrimPrefix(verb, "sparkwing ")
	return fmt.Errorf(
		"%s: run %s lives in the standalone store %s, and %s writes to the store that holds the run; "+
			"point sparkwing at that store:\n  SPARKWING_HOME=%s sparkwing %s --run %s",
		verb, runID, path, verb, filepath.Dir(path), remedy, runID)
}
