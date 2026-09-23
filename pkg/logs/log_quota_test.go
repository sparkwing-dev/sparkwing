package logs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/storagequota"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

// An allowance of 1600 bytes leaves a free team's logs a 300-byte share.
func newLogQuotaFixture(t *testing.T) *archiveFixture {
	t.Helper()
	return newArchiveFixtureWith(t, 0, map[string]storagequota.Standing{
		"team-a": {Tier: storagequota.TierFree, AllowanceBytes: 1600},
		"team-b": {Tier: storagequota.TierFree, AllowanceBytes: 1600},
	})
}

func (f *archiveFixture) logSize(t *testing.T, runID, nodeID string) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(f.root, "runs", runID, nodeID+".log"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func line(n int) string { return strings.Repeat("x", n-1) + "\n" }

// The logs service holds a free team to its share in the process that
// writes, and an append it refuses writes nothing.
func TestAFreeTeamsLogsStopAtTheirShare(t *testing.T) {
	f := newLogQuotaFixture(t)
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(200)); code != http.StatusNoContent {
		t.Fatalf("200 of 300 = %d %s", code, body)
	}
	code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(101))
	if code != http.StatusRequestEntityTooLarge || !strings.Contains(body, "add credits") {
		t.Fatalf("101 more past the share = %d %q, want 413 naming the remedy", code, body)
	}
	if got := f.logSize(t, "run-a", "build"); got != 200 {
		t.Fatalf("the log holds %d bytes after a refused append, want 200", got)
	}
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(100)); code != http.StatusNoContent {
		t.Fatalf("the last 100 bytes = %d %s", code, body)
	}
}

// A write that fails after its reservation gives the reservation back, so
// the team keeps the rest of its share: nothing is charged for bytes that
// were never written.
func TestAFailedAppendHoldsNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through a read-only directory")
	}
	f := newLogQuotaFixture(t)
	if code, _ := f.do(t, http.MethodPost, "/api/v1/logs/run-b/first", "Bearer b", line(10)); code != http.StatusNoContent {
		t.Fatalf("first append = %d", code)
	}
	dir := filepath.Join(f.root, "runs", "run-b")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	code, _ := f.do(t, http.MethodPost, "/api/v1/logs/run-b/second", "Bearer b", line(250))
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if code != http.StatusInternalServerError {
		t.Fatalf("an append the volume refused = %d, want 500", code)
	}
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-b/third", "Bearer b", line(290)); code != http.StatusNoContent {
		t.Fatalf("the rest of the share after a failed append = %d %s", code, body)
	}
}

// Bytes stay counted while they exist: archiving a run moves them from the
// volume's count to the archive's, and only deleting the run gives the room
// back.
func TestLogBytesAreReleasedOnlyWhenTheyAreDeleted(t *testing.T) {
	f := newLogQuotaFixture(t)
	ctx := context.Background()
	if code, _ := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(300)); code != http.StatusNoContent {
		t.Fatalf("the whole share = %d", code)
	}
	f.age(t, "run-a", time.Now().Add(-time.Hour))
	if n, err := f.srv.ArchiveOnce(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("archive = %d, %v", n, err)
	}
	if err := f.srv.MeasureVolumeUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if f.onVolume("run-a") {
		t.Fatal("the archived run is still on the volume")
	}
	if code, _ := f.do(t, http.MethodPost, "/api/v1/logs/run-a2/build", "Bearer a", line(1)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a byte past the share once the run was archived = %d, want 413", code)
	}
	if code, body := f.do(t, http.MethodDelete, "/api/v1/logs/run-a", "Bearer a", ""); code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", code, body)
	}
	if err := f.srv.MeasureVolumeUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a3/build", "Bearer a", line(300)); code != http.StatusNoContent {
		t.Fatalf("the whole share after the delete = %d %s", code, body)
	}
}

// A restored run matches its archive, so reading it back counts nothing
// twice.
func TestARestoredRunIsCountedOnce(t *testing.T) {
	f := newLogQuotaFixture(t)
	ctx := context.Background()
	if code, _ := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(200)); code != http.StatusNoContent {
		t.Fatal(code)
	}
	f.age(t, "run-a", time.Now().Add(-time.Hour))
	if _, err := f.srv.ArchiveOnce(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.do(t, http.MethodGet, "/api/v1/logs/run-a/build", "Bearer a", ""); code != http.StatusOK {
		t.Fatalf("read = %d", code)
	}
	if !f.onVolume("run-a") {
		t.Fatal("the read did not restore the run")
	}
	if err := f.srv.MeasureVolumeUsage(ctx); err != nil {
		t.Fatal(err)
	}
	if code, body := f.do(t, http.MethodPost, "/api/v1/logs/run-a2/build", "Bearer a", line(100)); code != http.StatusNoContent {
		t.Fatalf("the last 100 bytes with the run restored = %d %s", code, body)
	}
}

// A logs service that holds teams to a share counts its archive before it
// serves, so the first append after a start is judged against what the
// archive already holds.
func TestTheLogsServiceCountsItsArchiveBeforeItServes(t *testing.T) {
	f := newLogQuotaFixture(t)
	if _, err := f.raw.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(archiveBucket), Key: aws.String("logs/teams/team-a/runs/old/build.log"),
		Body: strings.NewReader(strings.Repeat("o", 300)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.RestoreArchive(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if code, _ := f.do(t, http.MethodPost, "/api/v1/logs/run-a/build", "Bearer a", line(1)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a byte past what the archive already holds, right after start = %d, want 413", code)
	}
}

// A logs service that cannot count its archive refuses to start rather than
// serve appends against an empty count.
func TestALogsServiceThatCannotCountItsArchiveRefusesToStart(t *testing.T) {
	fake := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(fake.Close)
	store, err := teamblob.New(teamblob.Options{
		Bucket: archiveBucket, Prefix: "logs", ReconcileAtStart: true,
		Client: s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(fake.URL), UsePathStyle: true,
			Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = ServeWith(ctx, ServeOptions{
		Root: t.TempDir(), Addr: "127.0.0.1:0", ControllerURL: "http://controller.invalid",
		Archive: &ArchiveOptions{Store: store},
	})
	if err == nil || !strings.Contains(err.Error(), "count") {
		t.Fatalf("ServeWith with an unlistable archive = %v, want a refusal to start naming the count", err)
	}
}
