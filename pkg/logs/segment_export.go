package logs

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
	Logger      *slog.Logger
}

// ExportReport counts what an export wrote. Failed names every run that
// could not be converted, with why.
type ExportReport struct {
	Runs     int
	Attempts int
	Skipped  int
	Failed   map[string]error
}

// Export writes every node log the service holds, on its volume and in
// its archive, as segments sealed by a manifest in the layout the
// controller reads: one attempt per stored attempt file, the retention
// class from whether the run's team had credits, the manifest's status
// from the service's seals. An attempt that already has a manifest at
// the destination is skipped, so an export may run again. It returns an
// error when any run failed.
func Export(ctx context.Context, opts ExportOptions) (ExportReport, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	report := ExportReport{Failed: map[string]error{}}
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
			if !e.IsDir() || validateID(e.Name()) != nil {
				continue
			}
			runID := e.Name()
			onVolume[runID] = true
			run, err := volumeRun(root, runID)
			if err == nil {
				err = exportRun(ctx, opts, run, &report)
			}
			if err != nil {
				report.Failed[runID] = err
			}
		}
	}
	if opts.Archive != nil {
		indexes, err := opts.Archive.List(ctx, "", indexRunsRel)
		if err != nil {
			return report, fmt.Errorf("list archived runs: %w", err)
		}
		for _, o := range indexes {
			runID := strings.TrimSuffix(strings.TrimPrefix(o.Rel, indexRunsRel), ".json")
			if onVolume[runID] || validateID(runID) != nil {
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
	}
	if len(report.Failed) > 0 {
		return report, fmt.Errorf("%d run(s) failed to export", len(report.Failed))
	}
	return report, nil
}

type exportSource struct {
	id    string
	team  string
	free  bool
	files []string
	read  func(rel string) ([]byte, error)
}

func volumeRun(root *os.Root, runID string) (exportSource, error) {
	meta := readRunMeta(root, runID)
	files, err := runFiles(root, runID)
	if err != nil {
		return exportSource{}, err
	}
	src := exportSource{id: runID, team: meta.Team, free: meta.Free, read: func(rel string) ([]byte, error) {
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
	src := exportSource{id: runID, team: idx.Team, free: idx.Free, read: func(rel string) ([]byte, error) {
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

type exportAttempt struct {
	node                string
	file                string
	attempt, generation int64
}

// hack: the service flattens a node id's "/" to "__" in file names, so a node whose id holds "__" exports
// under the id with "/" in its place.
func attemptOfFile(rel string) (exportAttempt, bool) {
	nodeOf := func(name string) string {
		return strings.ReplaceAll(strings.TrimSuffix(name, ".log"), "__", "/")
	}
	parts := strings.Split(rel, "/")
	switch {
	case len(parts) == 1:
		return exportAttempt{node: nodeOf(parts[0]), file: rel, attempt: 1}, true
	case len(parts) == 3 && parts[0] == ".attempts":
		a := exportAttempt{node: nodeOf(parts[1]), file: rel}
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
	for _, a := range attempts {
		prefix, err := logseg.AttemptPrefix(class, team, run.id, a.node, a.attempt, a.generation)
		if err != nil {
			return fmt.Errorf("%s: %w", a.file, err)
		}
		if done, err := opts.To.Has(ctx, logseg.ManifestKey(prefix)); err != nil {
			return err
		} else if done {
			report.Skipped++
			continue
		}
		body, err := run.read(a.file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", a.file, err)
		}
		m := logseg.Manifest{
			Version: logseg.ManifestVersion, Run: run.id, Node: a.node, Attempt: a.attempt, Generation: a.generation,
			SealedBy: logseg.SealedByExport, SealedAt: time.Now().UTC(),
		}
		m.Status, m.Reason, m.DroppedLines = exportVerdict(seals[strings.ReplaceAll(a.node, "/", "__")], a.file, len(body) > 0)
		if err := writeExportSegments(ctx, opts.To, prefix, body, &m); err != nil {
			return fmt.Errorf("%s: %w", a.file, err)
		}
		report.Attempts++
	}
	report.Runs++
	return nil
}

func parseSealRecords(read func(string) ([]byte, error), rel string) ([]sealRecord, error) {
	data, err := read(rel)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
