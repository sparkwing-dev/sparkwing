package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type testBytes struct {
	blob.Store
	values      map[blob.Digest][]byte
	puts        []blob.PutRequest
	gets        []blob.GetRequest
	getErr      error
	closeErr    error
	closeDigest blob.Digest
	onPut       func()
	onGet       func(context.Context, blob.GetRequest)
	onClose     func(blob.Digest)
	receipt     func(blob.Receipt) blob.Receipt
}

func (b *testBytes) Put(_ context.Context, q blob.PutRequest) (blob.Receipt, error) {
	if b.onPut != nil {
		b.onPut()
	}
	contents, err := io.ReadAll(q.Body)
	if err != nil {
		return blob.Receipt{}, err
	}
	if int64(len(contents)) != q.Size || digestOf(contents) != q.Digest {
		return blob.Receipt{}, blob.ErrIntegrity
	}
	b.puts = append(b.puts, q)
	b.values[q.Digest] = bytes.Clone(contents)
	receipt := blob.Receipt{Identity: q.Identity, Digest: q.Digest}
	if b.receipt != nil {
		receipt = b.receipt(receipt)
	}
	return receipt, nil
}

func (b *testBytes) Get(ctx context.Context, q blob.GetRequest) (blob.VerifiedReader, error) {
	if b.onGet != nil {
		b.onGet(ctx, q)
	}
	b.gets = append(b.gets, q)
	if b.getErr != nil {
		return nil, b.getErr
	}
	contents, ok := b.values[q.Digest]
	if !ok {
		return nil, blob.ErrNotFound
	}
	r := &testVerified{Reader: bytes.NewReader(contents), want: q.Digest, onClose: b.onClose}
	if q.Digest == b.closeDigest {
		r.closeErr = b.closeErr
	}
	return r, nil
}

type testVerified struct {
	*bytes.Reader
	want     blob.Digest
	hash     []byte
	eof      bool
	closeErr error
	onClose  func(blob.Digest)
}

func (r *testVerified) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.hash = append(r.hash, p[:n]...)
	if err == io.EOF {
		r.eof = true
	}
	return n, err
}

func (r *testVerified) Close() error {
	if r.onClose != nil {
		r.onClose(r.want)
	}
	return r.closeErr
}

func (r *testVerified) Verified() bool { return r.eof && digestOf(r.hash) == r.want }

type testRecords struct {
	blob.RecordStore
	result    blob.RecordLookupResult
	commits   []blob.CommitRecordRequest
	commitErr error
	lookupErr error
	onLookup  func(context.Context)
	record    func(blob.Record) blob.Record
}

func (r *testRecords) CommitRecord(_ context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	r.commits = append(r.commits, q)
	if r.commitErr != nil {
		return blob.Record{}, r.commitErr
	}
	record := blob.Record{Key: q.Key, Digest: q.Digest, Source: q.Source}
	if r.record != nil {
		record = r.record(record)
	}
	return record, nil
}

func (r *testRecords) LookupRecord(ctx context.Context, _ blob.RecordLookup) (blob.RecordLookupResult, error) {
	if r.onLookup != nil {
		r.onLookup(ctx)
	}
	return r.result, r.lookupErr
}

func digestOf(b []byte) blob.Digest {
	s := sha256.Sum256(b)
	return blob.Digest(hex.EncodeToString(s[:]))
}

func testData() (Data, *testBytes, *testRecords) {
	b := &testBytes{values: make(map[blob.Digest][]byte)}
	r := &testRecords{}
	id := blob.UploadIdentity{Kind: blob.UploadJob, Team: "team", RunID: "run", Principal: "principal", CapabilityRef: "grant", Attempt: runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "build", Attempt: 1, Generation: 1}}}
	a := blob.Authority{Repo: "repo", Principal: id.Principal, CapabilityRef: id.CapabilityRef, Ref: blob.RefFacts{Scope: blob.RefScope{Ref: "branch"}}, Attempt: id.Attempt}
	return Data{Bytes: b, Records: r, Authority: a, Identity: id, Pipeline: "pipeline", Budget: blob.Budget{Deadline: time.Now().Add(time.Minute)}}, b, r
}

func TestCaptureCommitsVerifiedMembers(t *testing.T) {
	d, b, r := testData()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(ws, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := Capture(t.Context(), d, ws, []string{"*.txt"}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.puts) != 2 || len(r.commits) != 1 {
		t.Fatalf("puts %d, commits %d", len(b.puts), len(r.commits))
	}
	q := r.commits[0]
	if q.Key.Kind != blob.KindManifest || q.Digest != record.Digest || len(q.Members) != 1 {
		t.Fatalf("commit: %+v", q)
	}
	if q.Key != blob.ResultRecordKey(d.Identity.Attempt.Value, d.Authority.Repo, d.Authority.Ref.Scope, d.Pipeline, blob.KindManifest) {
		t.Fatalf("record key: %+v", q.Key)
	}
	if m := q.Members[0]; m.Path != "a.txt" || m.Size != 5 || m.Mode != uint32(info.Mode().Perm()) || m.Digest != digestOf([]byte("alpha")) || m.ManifestDigest != q.Digest {
		t.Fatalf("member: %+v", m)
	}
	if b.puts[0].Digest != q.Members[0].Digest || b.puts[1].Digest != q.Digest {
		t.Fatalf("upload order: %+v", b.puts)
	}
	if len(b.values) != 2 {
		t.Fatalf("stored %d values", len(b.values))
	}
}

func TestCaptureRefusesFailedManifestCommit(t *testing.T) {
	d, b, r := testData()
	r.commitErr = blob.ErrDigestNotReadable
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(t.Context(), d, ws, []string{"a"}, func(string) {}); !errors.Is(err, blob.ErrDigestNotReadable) {
		t.Fatalf("commit error: %v", err)
	}
	if len(b.puts) != 2 || len(r.commits) != 1 {
		t.Fatalf("puts %d, commits %d", len(b.puts), len(r.commits))
	}
}

func TestCaptureUsesTheBytesItHashedAfterPathReplacement(t *testing.T) {
	d, b, r := testData()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "out"), []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	b.onPut = func() {
		if called {
			return
		}
		called = true
		swap := filepath.Join(t.TempDir(), "replacement")
		if err := os.WriteFile(swap, []byte("second"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(swap, filepath.Join(ws, "out")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Capture(t.Context(), d, ws, []string{"out"}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	m := r.commits[0].Members[0]
	if m.Digest != digestOf([]byte("first")) || string(b.values[m.Digest]) != "first" {
		t.Fatalf("captured mutated path: %+v, %q", m, b.values[m.Digest])
	}
}

func TestCaptureRefusesMutationDuringUpload(t *testing.T) {
	d, b, r := testData()
	ws := t.TempDir()
	path := filepath.Join(ws, "out")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	mutated := false
	b.onPut = func() {
		if mutated {
			return
		}
		mutated = true
		if err := os.WriteFile(path, []byte("other"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Capture(t.Context(), d, ws, []string{"out"}, func(string) {}); !errors.Is(err, blob.ErrIntegrity) {
		t.Fatalf("mutation accepted: %v", err)
	}
	if len(r.commits) != 0 {
		t.Fatalf("committed mutated bytes: %+v", r.commits)
	}
}

func TestCaptureMatchesDirectoryAndKeepsSortedPaths(t *testing.T) {
	d, _, r := testData()
	ws := t.TempDir()
	for _, name := range []string{"dist/z", "dist/sub/a", "other"} {
		path := filepath.Join(ws, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Capture(t.Context(), d, ws, []string{"dist"}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	members := r.commits[0].Members
	if len(members) != 2 || members[0].Path != "dist/sub/a" || members[1].Path != "dist/z" {
		t.Fatalf("members: %+v", members)
	}
}

func TestCaptureUploadsIdenticalBytesForEachMember(t *testing.T) {
	d, b, r := testData()
	ws := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(ws, name), []byte("same"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Capture(t.Context(), d, ws, []string{"*"}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if len(b.puts) != 3 || len(r.commits[0].Members) != 2 {
		t.Fatalf("puts %d, members %d", len(b.puts), len(r.commits[0].Members))
	}
	if b.puts[0].Digest != b.puts[1].Digest {
		t.Fatalf("identical content has different digests: %q, %q", b.puts[0].Digest, b.puts[1].Digest)
	}
}

func TestCaptureOutsideSymlinkPolicy(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(ws, "secret")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	d, _, r := testData()
	if _, err := Capture(t.Context(), d, ws, []string{"secret"}, func(string) {}); !errors.Is(err, errArtifactOutsideWorkspace) {
		t.Fatalf("literal escape: %v", err)
	}
	warned := false
	if _, err := Capture(t.Context(), d, ws, []string{"*"}, func(string) { warned = true }); err != nil {
		t.Fatal(err)
	}
	if !warned || len(r.commits) != 1 || len(r.commits[0].Members) != 0 {
		t.Fatalf("wildcard outside match: warned=%v, commits=%+v", warned, r.commits)
	}
}

func TestGlobMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"dist/**", "dist/a", true},
		{"dist/**", "dist/sub/b", true},
		{"dist/**", "other/a", false},
		{"a/**/c", "a/c", true},
		{"a/**/c", "a/x/y/c", true},
		{"*.json", "sub/x.json", false},
	} {
		if got := globMatch(tc.pattern, tc.name); got != tc.want {
			t.Errorf("globMatch(%q,%q)=%v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}
