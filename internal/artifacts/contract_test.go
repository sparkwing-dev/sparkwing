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
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type testBytes struct {
	blob.Store
	values map[blob.Digest][]byte
	puts   []blob.PutRequest
	gets   []blob.GetRequest
	getErr error
	onPut  func()
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
	return blob.Receipt{Identity: q.Identity, Digest: q.Digest}, nil
}

func (b *testBytes) Get(_ context.Context, q blob.GetRequest) (blob.VerifiedReader, error) {
	b.gets = append(b.gets, q)
	if b.getErr != nil {
		return nil, b.getErr
	}
	contents, ok := b.values[q.Digest]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return &testVerified{Reader: bytes.NewReader(contents), want: q.Digest}, nil
}

type testVerified struct {
	*bytes.Reader
	want blob.Digest
	hash []byte
	eof  bool
}

func (r *testVerified) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.hash = append(r.hash, p[:n]...)
	if err == io.EOF {
		r.eof = true
	}
	return n, err
}
func (r *testVerified) Close() error   { return nil }
func (r *testVerified) Verified() bool { return r.eof && digestOf(r.hash) == r.want }

type testRecords struct {
	blob.RecordStore
	result    blob.RecordLookupResult
	commits   []blob.CommitRecordRequest
	commitErr error
	lookupErr error
}

func (r *testRecords) CommitRecord(_ context.Context, q blob.CommitRecordRequest) (blob.Record, error) {
	r.commits = append(r.commits, q)
	if r.commitErr != nil {
		return blob.Record{}, r.commitErr
	}
	return blob.Record{Key: q.Key, Digest: q.Digest}, nil
}
func (r *testRecords) LookupRecord(_ context.Context, _ blob.RecordLookup) (blob.RecordLookupResult, error) {
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
	if q.Key.Name != "run" || q.Key.Scope.Pipeline != "pipeline" || q.Key.Scope.Job != "build" || q.Key.Scope.Key != "" {
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

func TestStageRejectsUnauthorizedMissingAndCorruptMembers(t *testing.T) {
	for _, kind := range []string{"unauthorized", "missing", "corrupt", "short", "long", "manifest"} {
		t.Run(kind, func(t *testing.T) {
			d, b, r := testData()
			producer := t.TempDir()
			if err := os.WriteFile(filepath.Join(producer, "out"), []byte("fresh"), 0o644); err != nil {
				t.Fatal(err)
			}
			record, err := Capture(t.Context(), d, producer, []string{"out"}, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			q := r.commits[0]
			r.result = blob.RecordLookupResult{Found: true, Record: record, Members: q.Members}
			switch kind {
			case "unauthorized":
				r.lookupErr = blob.ErrDigestNotReadable
			case "missing":
				delete(b.values, q.Members[0].Digest)
			case "corrupt":
				b.values[q.Members[0].Digest] = []byte("wrong")
			case "short":
				b.values[q.Members[0].Digest] = []byte("fres")
			case "long":
				b.values[q.Members[0].Digest] = []byte("fresher")
			case "manifest":
				b.values[q.Digest] = []byte("bad")
			}
			consumer := t.TempDir()
			if err := os.WriteFile(filepath.Join(consumer, "out"), []byte("stale"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Stage(t.Context(), d, consumer, []Edge{{Record: record.Key}}); err == nil {
				t.Fatal("staged invalid record")
			}
			got, err := os.ReadFile(filepath.Join(consumer, "out"))
			if err != nil || string(got) != "stale" {
				t.Fatalf("destination mutated: %q, %v", got, err)
			}
			entries, err := os.ReadDir(consumer)
			if err != nil || len(entries) != 1 {
				t.Fatalf("partial staging files remain: %v, %v", entries, err)
			}
			if kind == "unauthorized" && len(b.gets) != 0 {
				t.Fatalf("read %d blobs before authorization", len(b.gets))
			}
		})
	}
}

func TestStageReplacesAfterVerification(t *testing.T) {
	d, _, r := testData()
	producer := t.TempDir()
	if err := os.WriteFile(filepath.Join(producer, "out"), []byte("fresh"), 0o755); err != nil {
		t.Fatal(err)
	}
	record, err := Capture(t.Context(), d, producer, []string{"out"}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	r.result = blob.RecordLookupResult{Found: true, Record: record, Members: r.commits[0].Members}
	consumer := t.TempDir()
	if err := os.WriteFile(filepath.Join(consumer, "out"), []byte("stale"), 0o444); err != nil {
		t.Fatal(err)
	}
	n, err := Stage(t.Context(), d, consumer, []Edge{{Record: record.Key}})
	if err != nil || n != 1 {
		t.Fatalf("stage: %d, %v", n, err)
	}
	got, err := os.ReadFile(filepath.Join(consumer, "out"))
	if err != nil || string(got) != "fresh" {
		t.Fatalf("content %q, %v", got, err)
	}
}

func TestStageRejectsEscapingMember(t *testing.T) {
	d, b, r := testData()
	body := []byte("bad")
	m := blob.Member{Team: "team", Path: "../escape", Digest: digestOf(body), Size: 3, Mode: 0o644}
	manifest := []byte(`{"entries":[{"path":"../escape","digest":"` + string(m.Digest) + `","size":3,"mode":420}]}`)
	md := digestOf(manifest)
	m.ManifestDigest = md
	b.values[md], b.values[m.Digest] = manifest, body
	key := blob.RecordKey{Team: "team", Kind: blob.KindManifest}
	r.result = blob.RecordLookupResult{Found: true, Record: blob.Record{Key: key, Digest: md}, Members: []blob.Member{m}}
	if _, err := Stage(t.Context(), d, t.TempDir(), []Edge{{Record: key}}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("escape: %v", err)
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

func TestStageRefusesSymlinkedParent(t *testing.T) {
	d, _, r := testData()
	producer := t.TempDir()
	if err := os.Mkdir(filepath.Join(producer, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(producer, "dist", "out"), []byte("fresh"), 0o644); err != nil {
		t.Fatal(err)
	}
	record, err := Capture(t.Context(), d, producer, []string{"dist"}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	r.result = blob.RecordLookupResult{Found: true, Record: record, Members: r.commits[0].Members}
	consumer, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(consumer, "dist")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Stage(t.Context(), d, consumer, []Edge{{Record: record.Key}}); err == nil {
		t.Fatal("wrote through a symlinked parent")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside mutated: %v, %v", entries, err)
	}
}

func TestStageAcceptsUnorderedAuthorizedMembers(t *testing.T) {
	d, _, r := testData()
	producer := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(producer, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	record, err := Capture(t.Context(), d, producer, []string{"*"}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	members := append([]blob.Member(nil), r.commits[0].Members...)
	members[0], members[1] = members[1], members[0]
	r.result = blob.RecordLookupResult{Found: true, Record: record, Members: members}
	consumer := t.TempDir()
	if n, err := Stage(t.Context(), d, consumer, []Edge{{Record: record.Key}}); err != nil || n != 2 {
		t.Fatalf("stage %d, %v", n, err)
	}
	for _, name := range []string{"a", "b"} {
		got, err := os.ReadFile(filepath.Join(consumer, name))
		if err != nil || string(got) != name {
			t.Fatalf("%s: %q, %v", name, got, err)
		}
	}
}

func TestStageMasksSpecialModeBits(t *testing.T) {
	d, b, _ := testData()
	contents := []byte("script")
	digest := digestOf(contents)
	b.values[digest] = contents
	ws := t.TempDir()
	root, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := stageMember(t.Context(), d, root, blob.Member{Digest: digest, Size: int64(len(contents)), Mode: uint32(os.ModeSetuid | os.ModeSetgid | 0o755)}, "script"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(ws, "script"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&^os.ModePerm != 0 {
		t.Fatalf("special mode bits retained: %v", info.Mode())
	}
}

func TestCheckPathsRejectsWorkspaceEscapes(t *testing.T) {
	for _, globs := range [][]string{{"../secret"}, {"/absolute"}} {
		if err := CheckPaths(globs, nil); err == nil {
			t.Fatalf("accepted outputs %q", globs)
		}
	}
	for _, into := range []string{"../other", "/absolute"} {
		if err := CheckPaths(nil, []Edge{{Into: into}}); err == nil {
			t.Fatalf("accepted prefix %q", into)
		}
	}
	if _, err := stageRel("vendor", "../escape"); err == nil {
		t.Fatal("accepted member traversal")
	}
}

func TestGlobMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"dist/**", "dist/a", true}, {"dist/**", "dist/sub/b", true},
		{"dist/**", "other/a", false}, {"a/**/c", "a/c", true},
		{"a/**/c", "a/x/y/c", true}, {"*.json", "sub/x.json", false},
	} {
		if got := globMatch(tc.pattern, tc.name); got != tc.want {
			t.Errorf("globMatch(%q,%q)=%v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}
