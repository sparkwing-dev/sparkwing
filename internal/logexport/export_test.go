package logexport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
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
	return ExportOptions{Root: root, Archive: archive, To: to, DefaultTeam: "default"}
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

	again, err := Export(ctx, opts)
	if err != nil || again.Attempts != 0 || again.Skipped != 4 {
		t.Fatalf("second export = %+v, %v", again, err)
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
