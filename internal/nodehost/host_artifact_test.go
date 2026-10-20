package nodehost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func artifactFixture() (ArtifactRecords, blob.RecordLookup, *artifactBackendFixture) {
	key := blob.RecordKey{Team: "team", Kind: blob.KindManifest, Name: "artifacts"}
	manifest, value := []byte("authoritative manifest bytes"), []byte("compiled executable\n")
	backend := newArtifactBackend(manifest, value)
	backend.lookup = blob.RecordLookupResult{Found: true, Record: blob.Record{Key: key, Digest: outputDigest(manifest)}, Members: []blob.Member{{Team: key.Team, ManifestDigest: outputDigest(manifest), Digest: outputDigest(value), Path: "bin/program", Size: int64(len(value)), Mode: 0o755}}}
	request := blob.RecordLookup{Key: key, Authority: blob.Authority{CapabilityRef: "live-consumer", Principal: "consumer"}}
	return ArtifactRecords{Blobs: backend, Records: backend}, request, backend
}

func TestArtifactStaging(t *testing.T) {
	t.Helper()
	t.Run("verified", func(t *testing.T) {
		stage, request, backend := artifactFixture()
		workspace := t.TempDir()
		count, err := stage.Stage(t.Context(), request, workspace, "dependencies")
		if err != nil || count != 1 {
			t.Fatalf("stage=%d, %v", count, err)
		}
		name := filepath.Join(workspace, "dependencies/bin/program")
		got, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(got, backend.value) {
			t.Fatalf("staged bytes=%q, %v", got, err)
		}
		info, err := os.Stat(name)
		if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
			t.Fatalf("staged executable=%v, %v", info, err)
		}
		if !reflect.DeepEqual(backend.request, request) || len(backend.gets) != 2 {
			t.Fatalf("changed lookup or reads: %+v, %+v", backend.request, backend.gets)
		}
		for _, get := range backend.gets {
			if get.Authority != request.Authority {
				t.Fatal("replaced reader authority")
			}
		}
		for _, reader := range backend.readers {
			if !reader.eof || !reader.closed {
				t.Fatal("required read did not verify EOF and close")
			}
		}
	})
	for _, mode := range []string{"lookup", "absent_record", "manifest_missing", "manifest_denied", "manifest_corrupt", "manifest_unverified", "manifest_close", "manifest_nil_reader", "manifest_reader_error", "member_missing", "member_denied", "member_corrupt", "member_unverified", "member_close", "member_nil_reader", "member_reader_error", "member_cancel", "wrong_size", "extra_bytes", "negative_size", "wrong_key", "wrong_team", "wrong_manifest", "invalid_digest", "duplicate_path", "file_parent", "empty_path", "parent_path", "absolute_path"} {
		t.Run(mode, func(t *testing.T) {
			stage, request, backend := artifactFixture()
			backend.mode = mode
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			backend.cancel = cancel
			switch mode {
			case "absent_record":
				backend.lookup.Found = false
			case "wrong_size":
				backend.lookup.Members[0].Size++
			case "extra_bytes":
				backend.lookup.Members[0].Size--
			case "negative_size":
				backend.lookup.Members[0].Size = -1
			case "wrong_key":
				backend.lookup.Record.Key.Name = "different"
			case "wrong_team":
				backend.lookup.Members[0].Team = "other"
			case "wrong_manifest":
				backend.lookup.Members[0].ManifestDigest = outputDigest([]byte("other"))
			case "invalid_digest":
				backend.lookup.Members[0].Digest = "not-a-digest"
			case "duplicate_path":
				backend.lookup.Members = append(backend.lookup.Members, backend.lookup.Members[0])
			case "file_parent":
				member := backend.lookup.Members[0]
				member.Path = "bin"
				backend.lookup.Members = append(backend.lookup.Members, member)
			case "empty_path":
				backend.lookup.Members[0].Path = ""
			case "parent_path":
				backend.lookup.Members[0].Path = "../escape"
			case "absolute_path":
				backend.lookup.Members[0].Path = filepath.ToSlash(filepath.Join(t.TempDir(), "escape"))
			}
			workspace := t.TempDir()
			if err := os.MkdirAll(filepath.Join(workspace, "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(workspace, "bin/program")
			if err := os.WriteFile(name, []byte("prior content"), 0o600); err != nil {
				t.Fatal(err)
			}
			count, err := stage.Stage(ctx, request, workspace, "")
			if err == nil || count != 0 {
				t.Fatalf("accepted %s: stage=%d, %v", mode, count, err)
			}
			got, err := os.ReadFile(name)
			if err != nil || string(got) != "prior content" {
				t.Fatalf("failed read replaced destination=%q, %v", got, err)
			}
			entries, err := os.ReadDir(filepath.Dir(name))
			if err != nil || len(entries) != 1 {
				t.Fatalf("staging leaked temporary files=%v, %v", entries, err)
			}
			for _, reader := range backend.readers {
				if !reader.closed {
					t.Fatal("failed read leaked reader")
				}
			}
		})
	}
	t.Run("masked_mode", func(t *testing.T) {
		stage, request, backend := artifactFixture()
		backend.lookup.Members[0].Mode = 0o107755
		workspace := t.TempDir()
		if count, err := stage.Stage(t.Context(), request, workspace, ""); err != nil || count != 1 {
			t.Fatalf("masked mode=%d, %v", count, err)
		}
		info, err := os.Stat(filepath.Join(workspace, "bin/program"))
		if err != nil || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
			t.Fatalf("staged mode=%v, %v", info, err)
		}
	})
	t.Run("empty_manifest", func(t *testing.T) {
		stage, request, backend := artifactFixture()
		backend.lookup.Members = nil
		count, err := stage.Stage(t.Context(), request, t.TempDir(), "")
		if err != nil || count != 0 || len(backend.gets) != 1 {
			t.Fatalf("empty manifest=%d, %v, reads=%d", count, err, len(backend.gets))
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		stage, request, backend := artifactFixture()
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(outputRefused)
		if _, err := stage.Stage(ctx, request, t.TempDir(), ""); !errors.Is(err, outputRefused) || len(backend.gets) != 0 || backend.request.Key.Kind != "" {
			t.Fatalf("cancelled read=%v, requests=%+v", err, backend.gets)
		}
	})
	for _, into := range []string{"..", "../escape", "/absolute"} {
		t.Run("prefix_"+into, func(t *testing.T) {
			stage, request, backend := artifactFixture()
			if _, err := stage.Stage(t.Context(), request, t.TempDir(), into); err == nil || backend.request.Key.Kind != "" {
				t.Fatalf("accepted prefix %q: %v", into, err)
			}
		})
	}
	t.Run("symlink_parent", func(t *testing.T) {
		stage, request, _ := artifactFixture()
		workspace, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(workspace, "bin")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := stage.Stage(t.Context(), request, workspace, ""); err == nil {
			t.Fatal("followed an escaping workspace symlink")
		}
		entries, err := os.ReadDir(outside)
		if err != nil || len(entries) != 0 {
			t.Fatalf("outside entries=%v, %v", entries, err)
		}
	})
	t.Run("symlink_destination", func(t *testing.T) {
		stage, request, backend := artifactFixture()
		workspace := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(workspace, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(workspace, "bin/program")
		if err := os.Symlink(outside, name); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if count, err := stage.Stage(t.Context(), request, workspace, ""); err != nil || count != 1 {
			t.Fatalf("replace link=%d, %v", count, err)
		}
		got, err := os.ReadFile(outside)
		if err != nil || string(got) != "private" {
			t.Fatalf("overwrote link target=%q, %v", got, err)
		}
		got, err = os.ReadFile(name)
		if err != nil || !bytes.Equal(got, backend.value) {
			t.Fatalf("staged bytes=%q, %v", got, err)
		}
	})
}

func TestExecutorArtifactStaging(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"verified", "member_missing", "member_corrupt", "manifest_close"} {
		t.Run(mode, func(t *testing.T) {
			_, request, backend := artifactFixture()
			request.Key.Scope = blob.Scope{Repo: "example.com/acme/repo", Pipeline: "build", Job: "producer"}
			backend.lookup.Record.Key = request.Key
			backend.mode = mode
			workspace := t.TempDir()
			var trace []string
			var processes []*scriptedSDK
			input, state, host := executionHostFixture(t, "artifact_retry", &trace, &processes, func(deps *ExecutorDependencies) {
				deps.BindStorage = func(_ context.Context, in NodeInput) (StorageBinding, error) {
					request.Authority.Attempt = runspec.Optional[runspec.AttemptIdentity]{Set: true, Value: runspec.AttemptIdentity{Team: "team", RunID: in.Accepted.RunID, NodeID: in.NodeID, Generation: in.Generation, Attempt: in.Attempt}}
					request.Authority.Repo = "example.com/acme/repo"
					return StorageBinding{Bytes: backend, Records: backend, Authority: request.Authority}, nil
				}
				stager := ArtifactRecords{Producer: func(_ context.Context, _ NodeInput, producer string) (blob.RecordKey, error) {
					if producer != request.Key.Scope.Job {
						return blob.RecordKey{}, errors.New("staging lost its declared producer")
					}
					return request.Key, nil
				}}
				deps.PrepareArtifacts = stager.Prepare
				start := deps.Start
				deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
					got, err := os.ReadFile(filepath.Join(workspace, "bin/program"))
					if err != nil || !bytes.Equal(got, backend.value) {
						return nil, errors.New("SDK started before required artifacts were staged")
					}
					return start(ctx, in, activity)
				}
			})
			acquire := host.Acquire
			host.Acquire = func(ctx context.Context, spec source.Spec, options source.Options) (*source.Entry, error) {
				entry, err := acquire(ctx, spec, options)
				if entry != nil {
					entry.Directory = workspace
				}
				return entry, err
			}
			err := host.Execute(t.Context(), input)
			if (err == nil) != (mode == "verified") {
				t.Fatalf("executor returned %v", err)
			}
			if len(state.reports) < 2 || state.reports[0].Cause != store.CauseStart {
				t.Fatalf("transitions=%+v", state.reports)
			}
			last := state.reports[len(state.reports)-1]
			if last.Cause != store.CauseReport || last.Extra.Result == nil {
				t.Fatalf("missing terminal report: %+v", last)
			}
			if mode == "verified" {
				if len(processes) != 3 || len(backend.gets) != 2 || last.Extra.Result.Outcome != store.NodeSuccess {
					t.Fatalf("attempts=%d reads=%d result=%+v", len(processes), len(backend.gets), last.Extra.Result)
				}
				for _, process := range processes {
					if !process.closed || !process.cleaned {
						t.Fatal("executor leaked SDK resources")
					}
				}
			} else if len(processes) != 0 || last.Extra.Result.Outcome != store.NodeFailed {
				t.Fatalf("failed stage started SDK or reported success: starts=%d, result=%+v", len(processes), last.Extra.Result)
			}
			if backend.request.Authority.Attempt.Value.Attempt != 1 {
				t.Fatal("staging used an authority from before the committed start")
			}
		})
	}
}
