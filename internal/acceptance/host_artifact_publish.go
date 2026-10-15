package acceptance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type artifactPublisher struct {
	blob.Store
	blob.RecordStore
	upload    nodehost.JobUpload
	mode      string
	puts      []blob.PutRequest
	values    map[blob.Digest][]byte
	transfers [][]byte
	commit    *blob.CommitRecordRequest
	beforePut func()
}

// CheckExecutorArtifactPublication connects capture to the executor's terminal
// result. Persistence, SDK work and storage authority use contract fakes.
func CheckExecutorArtifactPublication(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "put", "commit", "record", "body_failed", "result_refused"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			writePublishedFile(t, workspace, "dist/result", "compiled result", 0o755)
			_, adapter, backend := artifactPublicationFixture(t)
			backend.mode = mode
			var trace []string
			var processes []*executionSDK
			executionMode := "artifact_publish_retry"
			if mode == "body_failed" {
				executionMode = "artifact_publish_failed"
			}
			input, state, host := executionHostFixture(t, executionMode, &trace, &processes, func(deps *nodehost.ExecutorDependencies) {
				deps.BindStorage = func(_ context.Context, in nodehost.NodeInput) (nodehost.StorageBinding, error) {
					backend.upload.Identity.Attempt.Value = runspec.AttemptIdentity{Team: "team", RunID: in.Accepted.RunID, NodeID: in.NodeID, Generation: in.Generation, Attempt: in.Attempt}
					backend.upload.Authority.Attempt = backend.upload.Identity.Attempt
					backend.upload.Key = blob.ResultRecordKey(backend.upload.Identity.Attempt.Value, backend.upload.Authority.Repo, backend.upload.Authority.Ref.Scope, "build", blob.KindManifest)
					return nodehost.StorageBinding{Bytes: backend, Records: backend, Authority: backend.upload.Authority}, nil
				}
				deps.PrepareLayer = adapter.Prepare
			})
			acquire := host.Acquire
			host.Acquire = func(ctx context.Context, spec source.Spec, opts source.Options) (*source.Entry, error) {
				entry, err := acquire(ctx, spec, opts)
				if entry != nil {
					entry.Directory = workspace
				}
				return entry, err
			}
			if mode == "result_refused" {
				state.reject = store.CauseReport
			}
			err := host.Execute(t.Context(), input)
			wantError := mode != "success" && mode != "body_failed"
			if (err != nil) != wantError {
				t.Fatalf("execution=%v", err)
			}
			if len(processes) != 3 {
				t.Fatalf("attempts=%d", len(processes))
			}
			last := state.reports[len(state.reports)-1]
			result := last.Extra.Result
			if last.Cause != store.CauseReport || result == nil {
				t.Fatalf("report=%+v", last)
			}
			wantSuccess := mode == "success" || mode == "result_refused"
			if (result.Outcome == store.NodeSuccess) != wantSuccess || (result.ManifestRecord != nil) != wantSuccess {
				t.Fatalf("result=%+v", result)
			}
			if mode == "body_failed" {
				if len(backend.puts) != 0 || backend.commit != nil {
					t.Fatal("published failed job artifacts")
				}
			} else {
				for _, put := range backend.puts {
					if put.Identity.Attempt.Value.Attempt != 3 || put.Authority.Attempt.Value.Attempt != 3 {
						t.Fatal("published under a prior attempt")
					}
				}
				if wantSuccess && (len(backend.puts) != 2 || backend.commit == nil) {
					t.Fatalf("published %d times", len(backend.puts))
				}
			}
			for _, process := range processes {
				if !process.closed || !process.cleaned {
					t.Fatal("unjoined process cleanup")
				}
			}
		})
	}
	t.Run("validates_before_body", func(t *testing.T) {
		_, a, _ := artifactPublicationFixture(t)
		input := nodehost.NodeInput{Entry: &source.Entry{Directory: t.TempDir()}}
		if _, err := a.Prepare(t.Context(), nodehost.LayerSpec{Name: "artifacts"}, input, nodeproto.JobSpec{Artifacts: []string{"../outside"}}); err == nil {
			t.Fatal("accepted invalid capture path before execution")
		}
	})
}

func (p *artifactPublisher) Put(_ context.Context, request blob.PutRequest) (blob.Receipt, error) {
	p.puts = append(p.puts, request)
	if p.beforePut != nil {
		p.beforePut()
	}
	value, err := io.ReadAll(request.Body)
	if err != nil {
		return blob.Receipt{}, err
	}
	p.transfers = append(p.transfers, value)
	if request.Digest != outputDigest(value) || request.Size != int64(len(value)) {
		return blob.Receipt{}, blob.ErrIntegrity
	}
	if request.Identity != p.upload.Identity || request.Authority != p.upload.Authority {
		return blob.Receipt{}, blob.ErrDigestNotReadable
	}
	if p.mode == "put" || p.mode == "manifest_put" && len(p.puts) > 1 {
		return blob.Receipt{}, outputRefused
	}
	p.values[request.Digest] = value
	receipt := blob.Receipt{Identity: request.Identity, Digest: request.Digest}
	if p.mode == "receipt" {
		receipt.Identity.Attempt.Value.Generation++
	}
	return receipt, nil
}

func (p *artifactPublisher) CommitRecord(_ context.Context, request blob.CommitRecordRequest) (blob.Record, error) {
	p.commit = &request
	if p.mode == "commit" {
		return blob.Record{}, outputRefused
	}
	if request.Key != p.upload.Key || request.Source != p.upload.Identity || request.Authority != p.upload.Authority {
		return blob.Record{}, blob.ErrDigestNotReadable
	}
	if _, ok := p.values[request.Digest]; !ok {
		return blob.Record{}, blob.ErrNotFound
	}
	for _, member := range request.Members {
		value, ok := p.values[member.Digest]
		if !ok || member.ManifestDigest != request.Digest || member.Team != request.Key.Team || int64(len(value)) != member.Size {
			return blob.Record{}, blob.ErrIntegrity
		}
	}
	record := blob.Record{Key: request.Key, Digest: request.Digest, Source: request.Source}
	if p.mode == "record" {
		record.Source.Attempt.Value.Attempt++
	}
	return record, nil
}

func (p *artifactPublisher) LookupRecord(_ context.Context, request blob.RecordLookup) (blob.RecordLookupResult, error) {
	if p.commit == nil || request.Key != p.commit.Key {
		return blob.RecordLookupResult{}, nil
	}
	if request.Authority != p.upload.Authority {
		return blob.RecordLookupResult{}, blob.ErrDigestNotReadable
	}
	return blob.RecordLookupResult{Found: true, Record: blob.Record{Key: p.commit.Key, Digest: p.commit.Digest, Source: p.commit.Source}, Members: p.commit.Members}, nil
}

func (p *artifactPublisher) Get(_ context.Context, request blob.GetRequest) (blob.VerifiedReader, error) {
	if request.Authority != p.upload.Authority {
		return nil, blob.ErrDigestNotReadable
	}
	value, ok := p.values[request.Digest]
	if !ok {
		return nil, blob.ErrNotFound
	}
	return &outputReader{Reader: bytes.NewReader(value)}, nil
}

func artifactPublicationFixture(t *testing.T) (nodehost.NodeInput, nodehost.ArtifactRecords, *artifactPublisher) {
	t.Helper()
	input, _, backend, _ := outputFixture(t, "success")
	backend.upload.Key.Kind = blob.KindManifest
	p := &artifactPublisher{upload: backend.upload, values: map[blob.Digest][]byte{}}
	a := nodehost.ArtifactRecords{Blobs: p, Records: p, Bind: func(context.Context, nodehost.NodeInput) (nodehost.JobUpload, error) { return p.upload, nil }}
	return input, a, p
}

func writePublishedFile(t *testing.T, workspace, name, value string, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(workspace, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

// CheckArtifactPublication exercises capture and fenced publication against byte
// and record fakes. Native upload authorization remains separate evidence.
func CheckArtifactPublication(t *testing.T) {
	t.Helper()
	t.Run("files_and_manifest", func(t *testing.T) {
		input, a, p := artifactPublicationFixture(t)
		workspace := t.TempDir()
		writePublishedFile(t, workspace, "dist/a.txt", "alpha", 0o644)
		writePublishedFile(t, workspace, "dist/a/nested.txt", "nested", 0o644)
		writePublishedFile(t, workspace, "dist/sub/b.txt", "bravo", 0o755)
		writePublishedFile(t, workspace, "ignored.txt", "hidden", 0o644)
		key, err := a.Publish(t.Context(), input, workspace, []string{"dist/**", "dist"}, nil)
		if err != nil || key == nil || *key != p.upload.Key {
			t.Fatalf("publish=%v, %v", key, err)
		}
		if len(p.puts) != 4 || p.commit == nil || len(p.commit.Members) != 3 {
			t.Fatalf("puts=%d commit=%+v", len(p.puts), p.commit)
		}
		var names []string
		for _, member := range p.commit.Members {
			names = append(names, member.Path)
		}
		if !reflect.DeepEqual(names, []string{"dist/a.txt", "dist/a/nested.txt", "dist/sub/b.txt"}) {
			t.Fatalf("members=%v", names)
		}
		if _, ok := p.values[outputDigest([]byte("hidden"))]; ok {
			t.Fatal("uploaded an undeclared file")
		}
		restored := t.TempDir()
		count, err := a.Stage(t.Context(), blob.RecordLookup{Key: *key, Authority: p.upload.Authority}, restored, "dependencies")
		if err != nil || count != 3 {
			t.Fatalf("restage=%d, %v", count, err)
		}
		for name, want := range map[string]string{"dist/a.txt": "alpha", "dist/a/nested.txt": "nested", "dist/sub/b.txt": "bravo"} {
			value, err := os.ReadFile(filepath.Join(restored, "dependencies", filepath.FromSlash(name)))
			if err != nil || string(value) != want {
				t.Fatalf("restored %q=%q, %v", name, value, err)
			}
		}
	})
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprintf("inside_symlink_broken_%t", broken), func(t *testing.T) {
			input, a, p := artifactPublicationFixture(t)
			workspace := t.TempDir()
			if !broken {
				writePublishedFile(t, workspace, "content", "payload", 0o600)
			}
			if err := os.Symlink("content", filepath.Join(workspace, "result")); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			key, err := a.Publish(t.Context(), input, workspace, []string{"result"}, nil)
			if broken {
				if err == nil || key != nil || p.commit != nil {
					t.Fatal("published broken symlink")
				}
			} else if err != nil || key == nil || len(p.commit.Members) != 1 || !bytes.Equal(p.values[p.commit.Members[0].Digest], []byte("payload")) {
				t.Fatalf("internal link=%v, %v", key, err)
			}
		})
	}
	t.Run("empty_manifest", func(t *testing.T) {
		input, a, p := artifactPublicationFixture(t)
		key, err := a.Publish(t.Context(), input, t.TempDir(), []string{"missing/**"}, nil)
		if err != nil || key == nil || p.commit == nil || len(p.commit.Members) != 0 || len(p.puts) != 1 {
			t.Fatalf("empty publication=%v, %v, %+v", key, err, p.commit)
		}
	})
	t.Run("bound_storage", func(t *testing.T) {
		input, a, p := artifactPublicationFixture(t)
		a.Blobs, a.Records = nil, nil
		input.Storage = nodehost.StorageBinding{Bytes: p, Records: p, Authority: p.upload.Authority}
		key, err := a.Publish(t.Context(), input, t.TempDir(), nil, nil)
		if err != nil || key == nil || p.commit == nil {
			t.Fatalf("bound publication=%v, %v", key, err)
		}
	})
	for _, mode := range []string{"put", "manifest_put", "receipt", "commit", "record", "cancelled", "cancel_during_put", "bind", "foreign_attempt", "foreign_repo", "bound_authority", "partial_storage"} {
		t.Run(mode, func(t *testing.T) {
			input, a, p := artifactPublicationFixture(t)
			workspace := t.TempDir()
			writePublishedFile(t, workspace, "result", "value", 0o600)
			p.mode = mode
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			switch mode {
			case "cancelled":
				cancel(outputRefused)
			case "foreign_attempt":
				input.Attempt++
			case "foreign_repo":
				p.upload.Authority.Repo = "example.com/other/repo"
			case "bound_authority":
				input.Storage = nodehost.StorageBinding{Bytes: p, Records: p, Authority: p.upload.Authority}
				input.Storage.Authority.CapabilityRef = "old-authority"
			case "partial_storage":
				input.Storage = nodehost.StorageBinding{Bytes: p}
			case "bind":
				a.Bind = func(context.Context, nodehost.NodeInput) (nodehost.JobUpload, error) {
					return nodehost.JobUpload{}, outputRefused
				}
			case "cancel_during_put":
				p.beforePut = func() { cancel(outputRefused) }
			}
			key, err := a.Publish(ctx, input, workspace, []string{"result"}, nil)
			if err == nil || key != nil {
				t.Fatalf("accepted %s: %v, %v", mode, key, err)
			}
			if mode == "cancel_during_put" && !errors.Is(err, outputRefused) {
				t.Fatal("lost publication cancellation cause")
			}
			if mode != "commit" && mode != "record" && p.commit != nil {
				t.Fatal("committed after failed upload or binding")
			}
		})
	}
	t.Run("mutated_upload", func(t *testing.T) {
		input, a, p := artifactPublicationFixture(t)
		workspace := t.TempDir()
		writePublishedFile(t, workspace, "result", "original", 0o600)
		p.beforePut = func() { writePublishedFile(t, workspace, "result", "modified", 0o600) }
		if key, err := a.Publish(t.Context(), input, workspace, []string{"result"}, nil); !errors.Is(err, blob.ErrIntegrity) || key != nil || p.commit != nil {
			t.Fatalf("changed bytes=%v, %v", key, err)
		}
	})
	t.Run("symlink_changes_during_transfer", func(t *testing.T) {
		input, a, p := artifactPublicationFixture(t)
		workspace, outside := t.TempDir(), t.TempDir()
		writePublishedFile(t, workspace, "content", "payload", 0o600)
		writePublishedFile(t, outside, "private", "outside-private-content", 0o600)
		name := filepath.Join(workspace, "result")
		if err := os.Symlink("content", name); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		swapped := false
		p.beforePut = func() {
			if swapped {
				return
			}
			swapped = true
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, "private"), name); err != nil {
				t.Fatal(err)
			}
		}
		key, err := a.Publish(t.Context(), input, workspace, []string{"result"}, nil)
		if err != nil || key == nil || !swapped {
			t.Fatalf("changed symlink=%v, %v", key, err)
		}
		for _, value := range p.transfers {
			if bytes.Contains(value, []byte("outside-private-content")) {
				t.Fatal("transferred outside file after link changed")
			}
		}
	})
	for _, pattern := range []string{"../escape", "/absolute", "[broken", ""} {
		t.Run("pattern_"+pattern, func(t *testing.T) {
			input, a, p := artifactPublicationFixture(t)
			if key, err := a.Publish(t.Context(), input, t.TempDir(), []string{pattern}, nil); err == nil || key != nil || len(p.puts) != 0 {
				t.Fatalf("accepted pattern %q: %v, %v", pattern, key, err)
			}
		})
	}
	for _, literal := range []bool{false, true} {
		t.Run("symlinks", func(t *testing.T) { CheckArtifactCaptureConfinement(t, "../", literal) })
	}
}

// CheckArtifactCaptureConfinement is shared with the fuzz driver. Outside bytes
// must never reach uploads through any generated symlink spelling.
func CheckArtifactCaptureConfinement(t *testing.T, spelling string, literal bool) {
	t.Helper()
	input, a, p := artifactPublicationFixture(t)
	workspace, outside := t.TempDir(), t.TempDir()
	private := "private-outside-artifact-content"
	writePublishedFile(t, outside, "private", private, 0o600)
	directory := "dir-" + string(outputDigest([]byte(spelling)))[:12]
	if err := os.Mkdir(filepath.Join(workspace, directory), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "private")
	if strings.Contains(spelling, "relative") || len(spelling)%2 == 1 {
		var err error
		target, err = filepath.Rel(filepath.Join(workspace, directory), target)
		if err != nil {
			t.Fatal(err)
		}
		target = strings.Repeat("."+string(filepath.Separator), len(spelling)%8) + target
	}
	for hop := 0; hop < len(spelling)%16; hop++ {
		name := fmt.Sprintf("hop-%d", hop)
		if err := os.Symlink(target, filepath.Join(workspace, directory, name)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		target = name
	}
	name := directory + "/result"
	if err := os.Symlink(target, filepath.Join(workspace, filepath.FromSlash(name))); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	pattern := "**"
	if literal {
		pattern = name
	}
	warnings := 0
	_, err := a.Publish(t.Context(), input, workspace, []string{pattern}, func(message string) {
		warnings++
		if strings.Contains(message, outside) || strings.Contains(message, private) {
			t.Fatal("warning exposed outside data")
		}
	})
	if literal && err == nil {
		t.Fatal("accepted literal outside artifact")
	}
	if !literal && (err != nil || warnings == 0) {
		t.Fatalf("outside wildcard=%v warnings=%d", err, warnings)
	}
	for _, value := range p.transfers {
		if bytes.Contains(value, []byte(private)) {
			t.Fatal("uploaded outside workspace")
		}
	}
}

// CheckArtifactUploadBinding requires every generated binding mismatch to fail
// before transfer. The backend remains responsible for live transaction authority.
func CheckArtifactUploadBinding(t *testing.T, field uint8, value string) {
	t.Helper()
	input, a, backend := artifactPublicationFixture(t)
	want := backend.upload
	actual := &backend.upload
	switch field % 18 {
	case 0:
		actual.Identity.Team = value
	case 1:
		actual.Identity.RunID = value
	case 2:
		actual.Identity.CapabilityRef = value
	case 3:
		actual.Identity.Principal = blob.Principal(value)
	case 4:
		actual.Identity.Attempt.Value.Attempt = int64(len(value))
	case 5:
		actual.Identity.Attempt.Value.Generation = int64(len(value))
	case 6:
		actual.Identity.Kind = blob.UploadIdentityKind(value)
	case 7:
		actual.Authority.CapabilityRef = value
	case 8:
		actual.Authority.Principal = blob.Principal(value)
	case 9:
		actual.Authority.Attempt.Value.NodeID = value
	case 10:
		actual.Authority.Repo = runspec.RepoKey(value)
	case 11:
		actual.Key.Team = value
	case 12:
		actual.Key.Scope.Job = value
	case 13:
		actual.Key.Scope.Pipeline = value
	case 14:
		actual.Key.Scope.Repo = runspec.RepoKey(value)
	case 15:
		actual.Key.Scope.Ref.Ref = value
	case 16:
		actual.Key.Name = value
	case 17:
		actual.Key.Scope.Key = value
	}
	key, err := a.Publish(t.Context(), input, t.TempDir(), nil, nil)
	if *actual == want {
		if err != nil || key == nil {
			t.Fatalf("refused matching binding: %v", err)
		}
	} else if err == nil || key != nil || len(backend.puts) != 0 || backend.commit != nil {
		t.Fatalf("mismatched binding reached storage: field=%d error=%v", field, err)
	}
}
