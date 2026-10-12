// Package logexport converts the node logs a sparkwing-logs service held,
// on its volume and in its archive, into the segments and manifests the
// controller reads. It reads the service's storage directly, so the
// service need not run.
package logexport

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ExportOptions names where an export reads and writes.
type ExportOptions struct {
	// Root is the logs service's storage root, the volume.
	Root string
	// Archive is the service's --archive-store, or nil for a service
	// that archives nothing.
	Archive *teamblob.Store
	// To receives the segments and manifests: the controller's cache
	// bucket and prefix, or its output directory.
	To storage.ArtifactStore
	// DefaultTeam is the team of a run whose files name none.
	DefaultTeam string
	// Runs is the controller's record of runs: each run is checked
	// against it and each converted attempt recorded in it.
	Runs Runs
	// DryRun counts what an export would write and writes nothing.
	DryRun bool
	Logger *slog.Logger
}

// Runs is the controller's run store as an export uses it.
type Runs interface {
	// RunNodes returns when team's run finished, or started if it has
	// not, and the IDs of its nodes. It returns an error wrapping
	// store.ErrNotFound when the team has no such run.
	RunNodes(ctx context.Context, team, runID string) (at time.Time, nodes []string, err error)
	// RecordLog records a converted attempt's log as sealed and last
	// written at at, so the controller prunes and deletes it with its run
	// as it does the logs its runners write.
	RecordLog(ctx context.Context, l store.NodeLog, at time.Time) error
}

// ExportReport counts what an export wrote. Failed names every run that
// could not be converted, with why.
type ExportReport struct {
	Runs     int
	Attempts int
	Bytes    int64
	Skipped  int
	Failed   map[string]error
}

// Export writes every node log the service holds, on its volume and in
// its archive, as segments sealed by a manifest in the layout the
// controller reads: one attempt per stored attempt file, the retention
// class from whether the run's team had credits, the manifest's status
// from the service's seals. A run on both the volume and in the archive
// takes each file from the volume and any the volume lost from the
// archive. An attempt that already has a manifest at the destination is
// skipped, so an export may run again. A run the controller does not
// hold, or with a file that cannot be read or filed under the team and
// node it belongs to, fails, naming the run; Export returns an error when
// any run failed.
func Export(ctx context.Context, opts ExportOptions) (ExportReport, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	report := ExportReport{Failed: map[string]error{}}
	if opts.Runs == nil {
		return report, errors.New("logexport: an export needs the controller's runs")
	}
	var archived []string
	inArchive := map[string]bool{}
	if opts.Archive != nil {
		indexes, err := opts.Archive.List(ctx, "", indexRunsRel)
		if err != nil {
			return report, fmt.Errorf("list archived runs: %w", err)
		}
		for _, o := range indexes {
			runID := strings.TrimSuffix(strings.TrimPrefix(o.Rel, indexRunsRel), ".json")
			if storage.SafeSegment(runID) == nil && !inArchive[runID] {
				archived = append(archived, runID)
				inArchive[runID] = true
			}
		}
	}
	root, err := os.OpenRoot(filepath.Join(opts.Root, "runs"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return report, err
	}
	onVolume := map[string]bool{}
	if root != nil {
		defer func() {
			if err := root.Close(); err != nil {
				opts.Logger.Warn("close the logs volume", "err", err)
			}
		}()
		entries, err := readDirAt(root, ".")
		if err != nil {
			return report, err
		}
		for _, e := range entries {
			if !e.IsDir() || storage.SafeSegment(e.Name()) != nil {
				continue
			}
			runID := e.Name()
			onVolume[runID] = true
			run, err := volumeRun(root, runID)
			if err == nil && inArchive[runID] {
				var arch exportSource
				if arch, err = archivedRun(ctx, opts.Archive, runID); err == nil {
					run, err = mergeSources(run, arch)
				}
			}
			if err == nil {
				err = exportRun(ctx, opts, run, &report)
			}
			if err != nil {
				report.Failed[runID] = err
			}
		}
	}
	for _, runID := range archived {
		if onVolume[runID] {
			continue
		}
		run, err := archivedRun(ctx, opts.Archive, runID)
		if err == nil {
			err = exportRun(ctx, opts, run, &report)
		}
		if err != nil {
			report.Failed[runID] = err
		}
	}
	if len(report.Failed) > 0 {
		return report, fmt.Errorf("%d run(s) failed to export", len(report.Failed))
	}
	return report, nil
}

// safety: read wraps os.ErrNotExist for a file the source no longer holds, which mergeSources takes from the
// archive instead; hasMeta is false for a volume run whose metadata file is gone, which then names no owner.
type exportSource struct {
	id      string
	team    string
	free    bool
	hasMeta bool
	files   []string
	read    func(rel string) ([]byte, error)
}

func volumeRun(root *os.Root, runID string) (exportSource, error) {
	meta, hasMeta, err := readRunMeta(root, runID)
	if err != nil {
		return exportSource{}, err
	}
	files, err := runFiles(root, runID)
	if err != nil {
		return exportSource{}, err
	}
	src := exportSource{id: runID, team: meta.Team, free: meta.Free, hasMeta: hasMeta, read: func(rel string) ([]byte, error) {
		return readLogFile(root, filepath.Join(runID, filepath.FromSlash(rel)))
	}}
	for _, f := range files {
		src.files = append(src.files, f.rel)
	}
	return src, nil
}

func archivedRun(ctx context.Context, archive *teamblob.Store, runID string) (exportSource, error) {
	data, err := archive.ReadAll(ctx, "", indexRunRel(runID))
	if err != nil {
		return exportSource{}, err
	}
	idx, err := parseRunIndex(runID, data)
	if err != nil {
		return exportSource{}, err
	}
	src := exportSource{id: runID, team: idx.Team, free: idx.Free, hasMeta: true, read: func(rel string) ([]byte, error) {
		data, err := archive.ReadAll(ctx, idx.Team, runObjectRel(runID, rel))
		if errors.Is(err, teamblob.ErrNotFound) {
			return nil, os.ErrNotExist
		}
		return data, err
	}}
	for _, f := range idx.Files {
		if safeArchivedRel(f.Rel) {
			src.files = append(src.files, f.Rel)
		}
	}
	return src, nil
}

// safety: the service archived a whole run before removing it from the volume, so a run found in both may have lost
// files from the volume, never from the archive; one whose two copies name different owners is refused.
func mergeSources(vol, arch exportSource) (exportSource, error) {
	if vol.hasMeta && (vol.team != arch.team || vol.free != arch.free) {
		return exportSource{}, fmt.Errorf("the volume records team %q (free %t) and the archive team %q (free %t)",
			vol.team, vol.free, arch.team, arch.free)
	}
	files := slices.Concat(vol.files, arch.files)
	slices.Sort(files)
	merged := arch
	merged.files = slices.Compact(files)
	merged.read = func(rel string) ([]byte, error) {
		data, err := vol.read(rel)
		if errors.Is(err, os.ErrNotExist) {
			return arch.read(rel)
		}
		return data, err
	}
	return merged, nil
}

// hack: stem is the node's file name, which the service wrote with each "/" of the node's ID as "__", so the node
// itself is known only once nodeOfStem matches it against the run's nodes.
type exportAttempt struct {
	stem, node          string
	file                string
	attempt, generation int64
}

func attemptOfFile(rel string) (exportAttempt, bool) {
	nodeOf := func(name string) string { return strings.TrimSuffix(name, ".log") }
	parts := strings.Split(rel, "/")
	switch {
	case len(parts) == 1:
		return exportAttempt{stem: nodeOf(parts[0]), file: rel, attempt: 1}, true
	case len(parts) == 3 && parts[0] == ".attempts":
		a := exportAttempt{stem: nodeOf(parts[1]), file: rel}
		var ordinal, generation int64
		switch {
		case scanned(parts[2], "a%020d_g%020d.log", &ordinal, &generation),
			scanned(parts[2], "a%020d_t%020d.log", &ordinal, &generation):
			a.attempt, a.generation = ordinal, generation
		case scanned(parts[2], "t_g%020d.log", &generation):
			a.attempt, a.generation = 1, generation
		default:
			return exportAttempt{}, false
		}
		return a, a.attempt > 0
	default:
		return exportAttempt{}, false
	}
}

// safety: "__" in a file name stands for either "/" or itself, so such a name takes the one node of the run it can
// name, and a name no node or several nodes fit is refused rather than filed under a node that may not exist.
func nodeOfStem(stem string, nodes []string) (string, error) {
	if !strings.Contains(stem, "__") {
		return stem, nil
	}
	var fits []string
	for _, n := range nodes {
		if strings.ReplaceAll(n, "/", "__") == stem {
			fits = append(fits, n)
		}
	}
	if len(fits) != 1 {
		return "", fmt.Errorf("log file %s.log names no single node of the run (the run's nodes that fit it: %q)", stem, fits)
	}
	return fits[0], nil
}

func scanned(name, format string, into ...any) bool {
	n, err := fmt.Sscanf(name, format, into...)
	return err == nil && n == len(into) && fmt.Sprintf(format, derefAll(into)...) == name
}

func derefAll(ptrs []any) []any {
	out := make([]any, len(ptrs))
	for i, p := range ptrs {
		out[i] = *(p.(*int64))
	}
	return out
}

func exportRun(ctx context.Context, opts ExportOptions, run exportSource, report *ExportReport) error {
	team := run.team
	if team == "" {
		team = opts.DefaultTeam
	}
	class := logseg.ClassStandard
	if run.free {
		class = logseg.ClassFree
	}
	at, nodes, err := opts.Runs.RunNodes(ctx, team, run.id)
	if err != nil {
		return fmt.Errorf("the controller's run %s of team %s: %w", run.id, team, err)
	}
	seals := map[string][]sealRecord{}
	var attempts []exportAttempt
	for _, rel := range run.files {
		if dir, name, ok := strings.Cut(rel, "/"); ok && dir == sealsDir {
			recs, err := parseSealRecords(run.read, rel)
			if err != nil {
				return err
			}
			seals[strings.TrimSuffix(name, ".log")] = recs
			continue
		}
		if a, ok := attemptOfFile(rel); ok {
			attempts = append(attempts, a)
		}
	}
	slices.SortFunc(attempts, func(x, y exportAttempt) int { return strings.Compare(x.file, y.file) })
	for i, a := range attempts {
		if attempts[i].node, err = nodeOfStem(a.stem, nodes); err != nil {
			return err
		}
	}
	for _, a := range attempts {
		node := a.node
		prefix, err := logseg.AttemptPrefix(class, team, run.id, node, a.attempt, a.generation)
		if err != nil {
			return fmt.Errorf("%s: %w", a.file, err)
		}
		rec := store.NodeLog{
			Team: store.Team(team), RunID: run.id, NodeID: node, Attempt: a.attempt, Generation: a.generation, Prefix: prefix,
		}
		if done, err := opts.To.Has(ctx, logseg.ManifestKey(prefix)); err != nil {
			return err
		} else if done {
			if !opts.DryRun {
				if err := recordConverted(ctx, opts, rec, at); err != nil {
					return fmt.Errorf("%s: %w", a.file, err)
				}
			}
			report.Skipped++
			continue
		}
		body, err := run.read(a.file)
		if err != nil {
			return fmt.Errorf("read %s: %w", a.file, err)
		}
		m := logseg.Manifest{
			Version: logseg.ManifestVersion, Run: run.id, Node: node, Attempt: a.attempt, Generation: a.generation,
			SealedBy: logseg.SealedByExport, SealedAt: time.Now().UTC(),
		}
		m.Status, m.Reason, m.DroppedLines = exportVerdict(seals[a.stem], a.file, len(body) > 0)
		if !opts.DryRun {
			if err := writeExportSegments(ctx, opts.To, prefix, body, &m); err != nil {
				return fmt.Errorf("%s: %w", a.file, err)
			}
			if err := recordConverted(ctx, opts, rec, at); err != nil {
				return fmt.Errorf("%s: %w", a.file, err)
			}
		}
		report.Attempts++
		report.Bytes += int64(len(body))
	}
	report.Runs++
	return nil
}

// safety: the record takes what the manifest at the destination names, which is the one a reader serves even when
// an earlier export wrote it.
func recordConverted(ctx context.Context, opts ExportOptions, rec store.NodeLog, at time.Time) error {
	log, err := logseg.Open(ctx, opts.To, rec.Prefix)
	if err != nil {
		return err
	}
	if log.Manifest == nil {
		return fmt.Errorf("%s has no manifest", rec.Prefix)
	}
	for _, seg := range log.Manifest.Segments {
		rec.StoredBytes += seg.StoredBytes
		rec.UploadedThrough = int64(seg.N)
	}
	return opts.Runs.RecordLog(ctx, rec, at)
}

func parseSealRecords(read func(string) ([]byte, error), rel string) ([]sealRecord, error) {
	data, err := read(rel)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	return decodeSealRecords(data), nil
}

// safety: a file no numbered stream claims never told the service whether it was whole, so it exports as
// incomplete rather than complete.
func exportVerdict(recs []sealRecord, file string, written bool) (status, reason string, dropped int64) {
	sealed := map[string]bool{}
	claimed := map[string]bool{}
	for _, r := range recs {
		if r.File == file {
			claimed[r.Stream] = true
		}
		if r.Kind == recordSeal {
			sealed[r.Stream] = true
			if r.File == file {
				lost := r.Missing
				if r.Seal != nil {
					lost = max(lost, r.Seal.Dropped)
				}
				dropped += lost
			}
		}
	}
	switch {
	case len(claimed) == 0 && written:
		return logseg.StatusIncomplete, logseg.ReasonMigratedUnsealed, 0
	case len(claimed) == 0:
		return logseg.StatusComplete, "", 0
	}
	for stream := range claimed {
		if !sealed[stream] {
			return logseg.StatusCutOff, logseg.ReasonUnsealed, dropped
		}
	}
	if dropped > 0 {
		return logseg.StatusIncomplete, logseg.ReasonSequenceGap, dropped
	}
	return logseg.StatusComplete, "", 0
}

// safety: a stored file can reach the 64 MiB node cap, past what a reader holds as one segment, so it is cut at
// line ends into segments no larger than logseg.SegmentBytes.
func writeExportSegments(ctx context.Context, to storage.ArtifactStore, prefix string, body []byte, m *logseg.Manifest) error {
	n := 0
	for len(body) > 0 {
		cut := min(len(body), logseg.SegmentBytes)
		if cut < len(body) {
			if i := bytes.LastIndexByte(body[:cut], '\n'); i >= 0 {
				cut = i + 1
			}
		}
		raw := body[:cut]
		body = body[cut:]
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		if _, err := zw.Write(raw); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		n++
		if err := to.Put(ctx, logseg.SegmentKey(prefix, n), bytes.NewReader(gz.Bytes())); err != nil {
			return err
		}
		lines := int64(bytes.Count(raw, []byte{'\n'}))
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			lines++
		}
		sum := sha256.Sum256(gz.Bytes())
		m.Segments = append(m.Segments, logseg.Segment{
			N: n, FirstSeq: m.Lines + 1, LastSeq: m.Lines + lines, Lines: lines,
			Bytes: int64(len(raw)), StoredBytes: int64(gz.Len()), SHA256: hex.EncodeToString(sum[:]),
		})
		m.Lines += lines
		m.Bytes += int64(len(raw))
	}
	if m.Lines > 0 {
		m.FirstSeq, m.LastSeq = 1, m.Lines
	}
	raw, err := m.Marshal()
	if err != nil {
		return err
	}
	if cw, ok := storage.Conditional(to); ok {
		_, err := cw.PutIfAbsent(ctx, logseg.ManifestKey(prefix), bytes.NewReader(raw))
		if errors.Is(err, storage.ErrPreconditionFailed) {
			return nil
		}
		return err
	}
	return to.Put(ctx, logseg.ManifestKey(prefix), bytes.NewReader(raw))
}
