package logs

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/fs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

// The export converts every node log the service holds, archived or on
// its volume, into segments the controller reads, with each attempt's
// completeness taken from the service's seals, and an export run again
// writes nothing twice.
func TestExportWritesSegmentsTheControllerReads(t *testing.T) {
	f := newArchiveFixture(t, 0)
	ctx := t.Context()
	for seq := int64(1); seq <= 3; seq++ {
		f.appendLine(t, "Bearer a", "run-a", "build", "s1", seq)
		f.appendLine(t, "Bearer a", "run-b", "build", "s2", seq)
	}
	if code := f.seal(t, "Bearer a", "run-a", "build", Seal{Stream: "s1", FinalSeq: 3, Lines: 3, Bytes: 21}); code != 204 {
		t.Fatalf("seal = %d", code)
	}
	f.appendLine(t, "Bearer a", "run-c", "test", "", 1)
	f.age(t, "run-a", time.Now().Add(-time.Hour))
	if n, err := f.srv.ArchiveOnce(ctx, time.Now()); err != nil || n == 0 || f.onVolume("run-a") {
		t.Fatalf("archive = %d, %v; run-a on volume %v", n, err, f.onVolume("run-a"))
	}

	to, err := fs.NewArtifactStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts := ExportOptions{Root: f.root, Archive: f.srv.archive.store, To: to, DefaultTeam: "default"}
	report, err := Export(ctx, opts)
	if err != nil || report.Runs != 3 || report.Attempts != 3 {
		t.Fatalf("export = %+v, %v", report, err)
	}

	for _, want := range []struct {
		run, node, status, reason, body string
	}{
		{"run-a", "build", logseg.StatusComplete, "", "line 1\nline 2\nline 3\n"},
		{"run-b", "build", logseg.StatusCutOff, logseg.ReasonUnsealed, "line 1\nline 2\nline 3\n"},
		{"run-c", "test", logseg.StatusIncomplete, logseg.ReasonMigratedUnsealed, "line 1\n"},
	} {
		found, err := logseg.FindAttempts(ctx, to, []string{logseg.ClassStandard, logseg.ClassFree}, "team-a", want.run, want.node)
		if err != nil || len(found) != 1 {
			t.Fatalf("%s/%s attempts = %+v, %v", want.run, want.node, found, err)
		}
		log, err := logseg.Open(ctx, to, found[0].Prefix)
		if err != nil || log.Manifest == nil {
			t.Fatalf("%s/%s: %+v, %v", want.run, want.node, log, err)
		}
		body, err := log.Read(ctx, to, storage.ReadOpts{})
		if err != nil || string(body) != want.body || log.Manifest.Status != want.status || log.Manifest.Reason != want.reason {
			t.Fatalf("%s/%s = %q %s %q, %v; want %q %s %q", want.run, want.node, body, log.Manifest.Status, log.Manifest.Reason, err,
				want.body, want.status, want.reason)
		}
	}

	again, err := Export(ctx, opts)
	if err != nil || again.Attempts != 0 || again.Skipped != 3 {
		t.Fatalf("second export = %+v, %v", again, err)
	}
}
