package logexport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const threeLines = "line 1\nline 2\nline 3\n"

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func records(t *testing.T, recs ...sealRecord) string {
	t.Helper()
	var b strings.Builder
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func newArchive(t *testing.T) *teamblob.Store {
	t.Helper()
	fake := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(fake.Close)
	client := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(fake.URL), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	})
	if _, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("archive")}); err != nil {
		t.Fatal(err)
	}
	store, err := teamblob.New(teamblob.Options{Bucket: "archive", Prefix: "logs", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type fakeRun struct {
	at    time.Time
	nodes []string
}

type fakeRuns struct {
	runs     map[string]fakeRun
	recorded map[string]store.NodeLog
	at       map[string]time.Time
}

func newFakeRuns() *fakeRuns {
	return &fakeRuns{runs: map[string]fakeRun{}, recorded: map[string]store.NodeLog{}, at: map[string]time.Time{}}
}

func (f *fakeRuns) add(team, runID string, at time.Time, nodes ...string) {
	f.runs[team+"/"+runID] = fakeRun{at: at, nodes: nodes}
}

func (f *fakeRuns) RunNodes(_ context.Context, team, runID string) (time.Time, []string, error) {
	r, ok := f.runs[team+"/"+runID]
	if !ok {
		return time.Time{}, nil, fmt.Errorf("run %s: %w", runID, store.ErrNotFound)
	}
	return r.at, r.nodes, nil
}

func (f *fakeRuns) RecordLog(_ context.Context, l store.NodeLog, at time.Time) error {
	f.recorded[l.Prefix], f.at[l.Prefix] = l, at
	return nil
}

var runAt = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// hack: hand-built rather than written by a running service, which no longer exists: run-b and run-c on the
// volume, run-d there with no team and no credits, run-a only in the archive.
func fixture(t *testing.T) ExportOptions {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	runs := filepath.Join(root, "runs")
	writeFile(t, filepath.Join(runs, "run-b", runMetaFile), `{"team":"team-a"}`)
	writeFile(t, filepath.Join(runs, "run-b", "build.log"), threeLines)
	writeFile(t, filepath.Join(runs, "run-b", sealsDir, "build.log"), records(t, sealRecord{Kind: "open", File: "build.log", Stream: "s2"}))
	writeFile(t, filepath.Join(runs, "run-c", runMetaFile), `{"team":"team-a"}`)
	writeFile(t, filepath.Join(runs, "run-c", "test.log"), "line 1\n")
	writeFile(t, filepath.Join(runs, "run-d", runMetaFile), `{"free":true}`)
	writeFile(t, filepath.Join(runs, "run-d", "deploy.log"), "line 1\n")

	archive := newArchive(t)
	put := func(team, rel, body string) {
		if _, err := archive.Put(ctx, team, rel, bytes.NewReader([]byte(body)), teamblob.PutOptions{Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
	}
	put("", indexRunRel("run-a"), `{"team":"team-a","files":[{"rel":"build.log"},{"rel":".seals/build.log"}]}`)
	put("team-a", runObjectRel("run-a", "build.log"), threeLines)
	put("team-a", runObjectRel("run-a", ".seals/build.log"), records(t,
		sealRecord{Kind: "open", File: "build.log", Stream: "s1"},
		sealRecord{Kind: recordSeal, File: "build.log", Stream: "s1", Seal: &seal{}}))

	to, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recorded := newFakeRuns()
	recorded.add("team-a", "run-a", runAt, "build")
	recorded.add("team-a", "run-b", runAt, "build")
	recorded.add("team-a", "run-c", runAt, "test")
	recorded.add("default", "run-d", runAt, "deploy")
	return ExportOptions{Root: root, Archive: archive, To: to, DefaultTeam: "default", Runs: recorded}
}

// The export converts every node log the service held, archived or on its
// volume, into segments the controller reads, with each attempt's
// completeness from the service's seals and its class from the team's
// standing; run again, it writes nothing twice.
func TestExportWritesSegmentsTheControllerReads(t *testing.T) {
	ctx := t.Context()
	opts := fixture(t)
	report, err := Export(ctx, opts)
	if err != nil || report.Runs != 4 || report.Attempts != 4 {
		t.Fatalf("export = %+v, %v", report, err)
	}
	classes := []string{logseg.ClassStandard, logseg.ClassFree}
	for _, want := range []struct {
		team, run, node, class, status, reason, body string
	}{
		{"team-a", "run-a", "build", logseg.ClassStandard, logseg.StatusComplete, "", threeLines},
		{"team-a", "run-b", "build", logseg.ClassStandard, logseg.StatusCutOff, logseg.ReasonUnsealed, threeLines},
		{"team-a", "run-c", "test", logseg.ClassStandard, logseg.StatusIncomplete, logseg.ReasonMigratedUnsealed, "line 1\n"},
		{"default", "run-d", "deploy", logseg.ClassFree, logseg.StatusIncomplete, logseg.ReasonMigratedUnsealed, "line 1\n"},
	} {
		found, err := logseg.FindAttempts(ctx, opts.To, classes, want.team, want.run, want.node)
		if err != nil || len(found) != 1 || !strings.HasPrefix(found[0].Prefix, logseg.ClassPrefix(want.class)) {
			t.Fatalf("%s/%s attempts = %+v, %v; want one under %s", want.run, want.node, found, err, want.class)
		}
		log, err := logseg.Open(ctx, opts.To, found[0].Prefix)
		if err != nil || log.Manifest == nil {
			t.Fatalf("%s/%s: %+v, %v", want.run, want.node, log, err)
		}
		body, err := log.Read(ctx, opts.To, storage.ReadOpts{})
		if err != nil || string(body) != want.body || log.Manifest.Status != want.status || log.Manifest.Reason != want.reason ||
			log.Manifest.SealedBy != logseg.SealedByExport {
			t.Fatalf("%s/%s = %q %+v, %v", want.run, want.node, body, log.Manifest, err)
		}
	}

	runs := opts.Runs.(*fakeRuns)
	if len(runs.recorded) != 4 {
		t.Fatalf("recorded %d attempts, want 4: %+v", len(runs.recorded), runs.recorded)
	}
	for prefix, rec := range runs.recorded {
		if rec.StoredBytes <= 0 || rec.UploadedThrough != 1 || !runs.at[prefix].Equal(runAt) {
			t.Fatalf("record of %s = %+v at %v; want its stored bytes, one segment, the run's time", prefix, rec, runs.at[prefix])
		}
	}

	clear(runs.recorded)
	again, err := Export(ctx, opts)
	if err != nil || again.Attempts != 0 || again.Skipped != 4 || len(runs.recorded) != 4 {
		t.Fatalf("second export = %+v, %v, recorded %d; want every attempt skipped and recorded", again, err, len(runs.recorded))
	}
}

func exportOne(t *testing.T, opts ExportOptions, run string) (ExportReport, error) {
	t.Helper()
	report, err := Export(t.Context(), opts)
	if err == nil {
		return report, nil
	}
	if report.Failed[run] == nil {
		t.Fatalf("export failed for %v, not %s", report.Failed, run)
	}
	return report, report.Failed[run]
}

func readBack(t *testing.T, opts ExportOptions, team, run, node string) string {
	t.Helper()
	ctx := t.Context()
	found, err := logseg.FindAttempts(ctx, opts.To, []string{logseg.ClassStandard, logseg.ClassFree}, team, run, node)
	if err != nil || len(found) != 1 {
		t.Fatalf("%s/%s attempts = %+v, %v; want one", run, node, found, err)
	}
	log, err := logseg.Open(ctx, opts.To, found[0].Prefix)
	if err != nil {
		t.Fatal(err)
	}
	body, err := log.Read(ctx, opts.To, storage.ReadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// A run the service archived and then only partly removed from its volume
// converts whole: each file the volume lost comes from the archive.
func TestExportTakesFilesTheVolumeLostFromTheArchive(t *testing.T) {
	ctx := t.Context()
	opts := fixture(t)
	writeFile(t, filepath.Join(opts.Root, "runs", "run-a", "deploy.log"), "volume line\n")
	if _, err := opts.Archive.Put(ctx, "", indexRunRel("run-a"),
		strings.NewReader(`{"team":"team-a","files":[{"rel":"build.log"},{"rel":".seals/build.log"},{"rel":"deploy.log"}]}`),
		teamblob.PutOptions{Size: -1}); err != nil {
		t.Fatal(err)
	}
	opts.Runs.(*fakeRuns).add("team-a", "run-a", runAt, "build", "deploy")
	if _, err := exportOne(t, opts, "run-a"); err != nil {
		t.Fatal(err)
	}
	if got := readBack(t, opts, "team-a", "run-a", "build"); got != threeLines {
		t.Fatalf("build, only in the archive = %q", got)
	}
	if got := readBack(t, opts, "team-a", "run-a", "deploy"); got != "volume line\n" {
		t.Fatalf("deploy, on the volume = %q", got)
	}
}

// Each way a run cannot be converted under the team and node it belongs to
// fails that run, naming it, rather than converting part of it.
func TestExportRefusesARunItCannotConvertWhole(t *testing.T) {
	for name, tc := range map[string]struct {
		run, want string
		setup     func(t *testing.T, opts ExportOptions)
	}{
		"indexed file missing from the archive": {"run-a", "build.log", func(t *testing.T, opts ExportOptions) {
			if err := opts.Archive.Delete(t.Context(), "team-a", runObjectRel("run-a", "build.log")); err != nil {
				t.Fatal(err)
			}
		}},
		"corrupt metadata": {"run-b", "metadata", func(t *testing.T, opts ExportOptions) {
			writeFile(t, filepath.Join(opts.Root, "runs", "run-b", runMetaFile), `{"team":`)
		}},
		"volume and archive disagree on the team": {"run-a", "team", func(t *testing.T, opts ExportOptions) {
			writeFile(t, filepath.Join(opts.Root, "runs", "run-a", runMetaFile), `{"team":"team-b"}`)
		}},
		"run the controller does not hold": {"run-c", "run-c", func(t *testing.T, opts ExportOptions) {
			delete(opts.Runs.(*fakeRuns).runs, "team-a/run-c")
		}},
		"node file no node of the run fits": {"run-c", "unit__test", func(t *testing.T, opts ExportOptions) {
			writeFile(t, filepath.Join(opts.Root, "runs", "run-c", "unit__test.log"), "line\n")
		}},
		"node file two nodes of the run fit": {"run-c", "unit__test", func(t *testing.T, opts ExportOptions) {
			writeFile(t, filepath.Join(opts.Root, "runs", "run-c", "unit__test.log"), "line\n")
			opts.Runs.(*fakeRuns).add("team-a", "run-c", runAt, "test", "unit/test", "unit__test")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			opts := fixture(t)
			tc.setup(t, opts)
			report, err := exportOne(t, opts, tc.run)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("export of %s = %v; want a failure naming %q", tc.run, err, tc.want)
			}
			if report.Runs != 3 {
				t.Fatalf("export reported %d runs converted, want the 3 others", report.Runs)
			}
		})
	}
}

// A node file name with "__" is filed under the run's one node it can
// name, whether that ID holds "__" or "/".
func TestExportFilesANodeUnderTheIDTheRunRecords(t *testing.T) {
	for _, node := range []string{"unit__test", "unit/test"} {
		t.Run(node, func(t *testing.T) {
			opts := fixture(t)
			writeFile(t, filepath.Join(opts.Root, "runs", "run-c", "unit__test.log"), "line\n")
			opts.Runs.(*fakeRuns).add("team-a", "run-c", runAt, "test", node)
			if _, err := exportOne(t, opts, "run-c"); err != nil {
				t.Fatal(err)
			}
			if got := readBack(t, opts, "team-a", "run-c", node); got != "line\n" {
				t.Fatalf("%s = %q", node, got)
			}
		})
	}
}

// A dry run counts what an export would write and writes nothing.
func TestExportDryRunCountsAndWritesNothing(t *testing.T) {
	ctx := t.Context()
	opts := fixture(t)
	opts.DryRun = true
	report, err := Export(ctx, opts)
	if err != nil || report.Runs != 4 || report.Attempts != 4 || report.Bytes != int64(2*len(threeLines)+2*len("line 1\n")) {
		t.Fatalf("dry run = %+v, %v", report, err)
	}
	if keys, err := opts.To.List(ctx, "logs/"); err != nil || len(keys) != 0 {
		t.Fatalf("dry run wrote %v, %v", keys, err)
	}
}
