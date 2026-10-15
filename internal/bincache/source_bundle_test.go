package bincache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestSourceBundleImportsExactSnapshotWithoutOrigin(t *testing.T) {
	checkSourceBundleImport(t, "", nil)
}

func TestSourceBundleImportsSHA1UnderSHA256Default(t *testing.T) {
	checkSourceBundleImport(t, "", map[string]string{"GIT_DEFAULT_HASH": "sha256"})
}

func TestSourceBundleRefusesSymlinkedSparkwing(t *testing.T) {
	for _, target := range []string{"external", "relative-external", "internal"} {
		t.Run(target, func(t *testing.T) { checkSourceBundleImport(t, target, nil) })
	}
}

func FuzzSourceBundleImportIgnoresAmbientGitControls(f *testing.F) {
	f.Add([]byte("trace-and-routing"))
	f.Add([]byte("config-and-filter"))
	f.Fuzz(func(t *testing.T, seed []byte) {
		sum := sha256.Sum256(seed)
		outside := t.TempDir()
		path := filepath.Join(outside, hex.EncodeToString(sum[:]))
		environment := map[string]string{
			"GIT_DIR": path, "GIT_WORK_TREE": path, "GIT_INDEX_FILE": path,
			"GIT_OBJECT_DIRECTORY": path, "GIT_ALTERNATE_OBJECT_DIRECTORIES": path,
			"GIT_COMMON_DIR": path, "GIT_ALLOW_PROTOCOL": "ext",
			"GIT_TRACE": path, "GIT_TRACE_CURL": path, "GIT_TRACE_PACKET": path,
			"GIT_TRACE2_EVENT": path, "GIT_CURL_VERBOSE": "1",
			"GIT_CONFIG_GLOBAL": path, "GIT_CONFIG_SYSTEM": path,
			"GIT_CONFIG": path, "GIT_CONFIG_PARAMETERS": "invalid config",
			"GIT_CONFIG_COUNT": "invalid count", "GIT_CONFIG_KEY_0": "core.hooksPath",
			"GIT_CONFIG_VALUE_0": path, "GIT_DEFAULT_HASH": "sha256",
		}
		checkSourceBundleImport(t, "", environment)
		assertEmptyDirectory(t, outside)
	})
}

func checkSourceBundleImport(t *testing.T, linkTarget string, environment map[string]string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=Sparkwing", "GIT_AUTHOR_EMAIL=workspace@sparkwing.dev",
			"GIT_COMMITTER_NAME=Sparkwing", "GIT_COMMITTER_EMAIL=workspace@sparkwing.dev",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	sourceDir := filepath.Join(repo, ".sparkwing")
	if linkTarget == "internal" {
		sourceDir = filepath.Join(repo, "pipeline-source")
	} else if linkTarget != "" {
		sourceDir = t.TempDir()
	}
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	git("init", "--object-format=sha1", "--quiet")
	if err := os.WriteFile(filepath.Join(sourceDir, "source.txt"), []byte("from bundle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if linkTarget != "" {
		target := sourceDir
		if linkTarget != "external" {
			var err error
			target, err = filepath.Rel(repo, sourceDir)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, filepath.Join(repo, ".sparkwing")); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "--quiet", "-m", "sparkwing working-tree snapshot")
	sha := git("rev-parse", "HEAD")
	git("update-ref", SeedRef(sha), sha)
	bundle := filepath.Join(t.TempDir(), "source.bundle")
	git("bundle", "create", bundle, SeedRef(sha))
	contents, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	data, transport := sourceBundleFixture(contents)
	workDir := t.TempDir()
	for name, value := range environment {
		t.Setenv(name, value)
	}
	got, err := FetchSourceBundle(t.Context(), data, transport.want, sha, "", workDir)
	if linkTarget != "" {
		if err == nil {
			t.Fatalf("imported symlinked source directory %s", got)
		}
		if !strings.Contains(err.Error(), "no .sparkwing directory") {
			t.Fatalf("symlink import error = %v, want source directory refusal", err)
		}
		assertEmptyDirectory(t, workDir)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(got, "source.txt"))
	if err != nil || !bytes.Equal(file, []byte("from bundle\n")) {
		t.Fatalf("imported source = %q, %v", file, err)
	}
	if err := os.WriteFile(filepath.Join(got, "source.txt"), []byte("existing checkout"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchSourceBundle(t.Context(), data, transport.want, sha, "", workDir); err == nil {
		t.Fatal("replaced an existing checkout")
	}
	file, err = os.ReadFile(filepath.Join(got, "source.txt"))
	if err != nil || string(file) != "existing checkout" {
		t.Fatalf("existing checkout = %q, %v", file, err)
	}
	entries, err := os.ReadDir(workDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "checkout" {
		t.Fatalf("staging remains: %v, %v", entries, err)
	}
	failedDir := t.TempDir()
	if _, err := FetchSourceBundle(t.Context(), data, transport.want, strings.Repeat("a", 40), "", failedDir); err == nil {
		t.Fatal("wrong snapshot commit imported")
	}
	assertEmptyDirectory(t, failedDir)
}

func sourceBundleFixture(body []byte) (*SourceData, *binaryBytes) {
	binary, records, transport := binaryFixture()
	sum := sha256.Sum256(body)
	transport.body, transport.want, transport.size = body, blob.Digest(hex.EncodeToString(sum[:])), int64(len(body))
	return &SourceData{Bytes: binary.Bytes, Records: records, Authority: binary.Authority, Identity: binary.Identity, Budget: binary.Budget}, transport
}

func assertEmptyDirectory(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("files remain: %v, %v", entries, err)
	}
}

func TestSourceBundleRefusesUnverifiedBytesBeforeGit(t *testing.T) {
	closeFailure := errors.New("close failed")
	for _, tc := range []struct {
		name   string
		change func(*SourceData, *binaryBytes)
		want   error
	}{
		{"corrupt", func(_ *SourceData, b *binaryBytes) { b.body = []byte("BUNDLE") }, blob.ErrIntegrity},
		{"short", func(_ *SourceData, b *binaryBytes) { b.body = []byte("bun") }, blob.ErrIntegrity},
		{"long", func(_ *SourceData, b *binaryBytes) { b.body = []byte("bundle!") }, blob.ErrIntegrity},
		{"unverified", func(d *SourceData, _ *binaryBytes) {
			d.Bytes = binaryStreamStore{reader: &binaryStream{Reader: bytes.NewBufferString("bundle")}}
		}, blob.ErrIntegrity},
		{"failed close", func(d *SourceData, _ *binaryBytes) {
			d.Bytes = binaryStreamStore{reader: &binaryStream{Reader: bytes.NewBufferString("bundle"), verified: true, closeErr: closeFailure}}
		}, closeFailure},
		{"missing", func(d *SourceData, _ *binaryBytes) { d.Bytes = binaryStreamStore{err: blob.ErrNotFound} }, blob.ErrNotFound},
		{"unreadable", func(d *SourceData, _ *binaryBytes) { d.Bytes = binaryStreamStore{err: blob.ErrDigestNotReadable} }, blob.ErrDigestNotReadable},
		{"budget", func(d *SourceData, _ *binaryBytes) { d.Budget.Deadline = time.Unix(1, 0) }, blob.ErrBudget},
		{"no budget", func(d *SourceData, _ *binaryBytes) { d.Budget = blob.Budget{} }, blob.ErrBudget},
		{"transfer deadline", func(d *SourceData, _ *binaryBytes) { d.Bytes = binaryStreamStore{err: context.DeadlineExceeded} }, blob.ErrBudget},
		{"stream deadline", func(d *SourceData, _ *binaryBytes) {
			d.Bytes = binaryStreamStore{reader: &binaryStream{Reader: sourceReadError{context.DeadlineExceeded}}}
		}, blob.ErrBudget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, transport := sourceBundleFixture([]byte("bundle"))
			tc.change(data, transport)
			dir := t.TempDir()
			_, err := FetchSourceBundle(t.Context(), data, transport.want, strings.Repeat("a", 40), "", dir)
			if !errors.Is(err, tc.want) {
				t.Fatalf("fetch = %v, want %v", err, tc.want)
			}
			assertEmptyDirectory(t, dir)
		})
	}
}

type sourceReadError struct{ err error }

func (r sourceReadError) Read([]byte) (int, error) { return 0, r.err }

func TestSourceBundleCancelledBeforeDownload(t *testing.T) {
	data, transport := sourceBundleFixture([]byte("bundle"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dir := t.TempDir()
	_, err := FetchSourceBundle(ctx, data, transport.want, strings.Repeat("a", 40), "", dir)
	if !errors.Is(err, context.Canceled) || transport.gets != 0 {
		t.Fatalf("fetch = %v, gets = %d", err, transport.gets)
	}
	assertEmptyDirectory(t, dir)
}

func TestSourceBundleUploadCommitsExactReceipt(t *testing.T) {
	data, transport := sourceBundleFixture(nil)
	records := &binaryRecords{}
	data.Records = records
	path := filepath.Join(t.TempDir(), "snapshot.bundle")
	if err := os.WriteFile(path, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	record, err := UploadSourceBundle(t.Context(), data, sha, path)
	if err != nil || len(records.commits) != 1 {
		t.Fatalf("upload = %v, commits = %v", err, records.commits)
	}
	q := records.commits[0]
	if q.Key.Kind != blob.KindSourceBundle || q.Key.Name != sha || q.Key.Team != data.Identity.Team || q.Key.Scope.Repo != data.Authority.Repo || q.Key.Scope.Ref != data.Authority.Ref.Scope || q.Source != data.Identity || q.Authority != data.Authority || record.Digest != q.Digest || string(transport.body) != "bundle" {
		t.Fatalf("wrong source record: %+v", q)
	}
	transport.putErr = blob.ErrIntegrity
	if _, err := UploadSourceBundle(t.Context(), data, sha, path); !errors.Is(err, blob.ErrIntegrity) || len(records.commits) != 1 {
		t.Fatalf("failed upload = %v, commits = %v", err, records.commits)
	}
}

type sourceRecordResponse struct {
	blob.RecordStore
	change func(*blob.Record)
}

func (r sourceRecordResponse) CommitRecord(ctx context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	record, err := r.RecordStore.CommitRecord(ctx, q)
	r.change(&record)
	return record, err
}

type sourceReceiptResponse struct {
	blob.Store
	receipt blob.Receipt
}

func (r sourceReceiptResponse) Put(context.Context, blob.PutRequest) (blob.Receipt, error) {
	return r.receipt, nil
}

func TestSourceBundleUploadRefusesMismatchedResponses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*SourceData)
		commits int
	}{
		{"receipt digest", func(d *SourceData) {
			d.Bytes = sourceReceiptResponse{receipt: blob.Receipt{Identity: d.Identity, Digest: "other"}}
		}, 0},
		{"receipt owner", func(d *SourceData) {
			sum := sha256.Sum256([]byte("bundle"))
			i := d.Identity
			i.RunID = "other"
			d.Bytes = sourceReceiptResponse{receipt: blob.Receipt{Identity: i, Digest: blob.Digest(hex.EncodeToString(sum[:]))}}
		}, 0},
		{"record digest", func(d *SourceData) {
			d.Records = sourceRecordResponse{d.Records, func(r *blob.Record) { r.Digest = "other" }}
		}, 1},
		{"record scope", func(d *SourceData) {
			d.Records = sourceRecordResponse{d.Records, func(r *blob.Record) { r.Key.Scope.Repo = "gh:456" }}
		}, 1},
		{"record owner", func(d *SourceData) {
			d.Records = sourceRecordResponse{d.Records, func(r *blob.Record) { r.Source.RunID = "other" }}
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := sourceBundleFixture(nil)
			records := &binaryRecords{}
			data.Records = records
			tc.change(data)
			path := filepath.Join(t.TempDir(), "bundle")
			if err := os.WriteFile(path, []byte("bundle"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := UploadSourceBundle(t.Context(), data, strings.Repeat("a", 40), path)
			if !errors.Is(err, blob.ErrIntegrity) || len(records.commits) != tc.commits {
				t.Fatalf("upload = %v, commits = %d", err, len(records.commits))
			}
		})
	}
}

func TestSourceBundleUploadRejectsAnotherOwner(t *testing.T) {
	data, transport := sourceBundleFixture(nil)
	data.Identity.Unit.Value.Generation++
	_, err := UploadSourceBundle(t.Context(), data, strings.Repeat("a", 40), "must-not-be-opened")
	if !errors.Is(err, blob.ErrDigestNotReadable) || transport.upload.Identity.Kind != "" {
		t.Fatalf("upload = %v, byte request = %+v", err, transport.upload)
	}
}
