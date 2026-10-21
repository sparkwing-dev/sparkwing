package nodehost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func TestRemoteDepcache(t *testing.T) {
	t.Helper()
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled_%t", enabled), func(t *testing.T) {
			input, adapter, backend, job := depcacheFixture(t)
			seedDepcache(t, input, adapter, backend, job)
			remote := newRemoteDirectoryCache(backend, blob.Record{Key: backend.commit.Key, Digest: backend.commit.Digest})
			remote.record.Key.Team = "remote-profile-team"
			backend.commit, backend.puts = nil, nil
			if enabled {
				input.Storage.RemoteCache = remote
			}
			finish, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job)
			if err != nil || finish == nil {
				t.Fatalf("prepare remote=%v", err)
			}
			if enabled {
				value, err := os.ReadFile(filepath.Join(input.Entry.Directory, "vendor/module/file"))
				if err != nil || string(value) != "downloaded dependency" || remote.lookups != 1 || remote.reads != 1 {
					t.Fatalf("remote restore=%q, %v, lookups=%d reads=%d", value, err, remote.lookups, remote.reads)
				}
				if remote.key.Repo != input.Storage.Authority.Repo || remote.key.Ref != input.Storage.Authority.Ref.Scope {
					t.Fatal("remote read widened its bound scope")
				}
			} else {
				if remote.lookups != 0 || remote.reads != 0 {
					t.Fatal("cache read ran without opt-in")
				}
				writePublishedFile(t, input.Entry.Directory, "vendor/module/file", "local dependency", 0o644)
			}
			result := store.NodeResult{Outcome: store.NodeSuccess}
			if err := finish(t.Context(), input, &result); err != nil {
				t.Fatal(err)
			}
			if enabled && (backend.commit != nil || len(backend.puts) != 0) {
				t.Fatal("remote hit caused a cache write")
			}
		})
	}
}

func TestExecutorDepcache(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "failed", "put", "commit", "miss_success", "miss_failed", "miss_put", "miss_commit"} {
		t.Run(mode, func(t *testing.T) {
			input, adapter, backend, job := depcacheFixture(t)
			workspace := input.Entry.Directory
			seedDepcache(t, input, adapter, backend, job)
			miss := strings.HasPrefix(mode, "miss_")
			behavior := strings.TrimPrefix(mode, "miss_")
			if miss {
				backend.commit = nil
			}
			backend.puts, backend.mode, backend.lookups = nil, behavior, 0
			var trace []string
			var processes []*scriptedSDK
			executionMode := "cache_retry"
			if behavior == "failed" {
				executionMode = "cache_failed"
			}
			originalPrepare := adapter.Prepare
			input, state, host := executionHostFixture(t, executionMode, &trace, &processes, func(deps *ExecutorDependencies) {
				start := deps.Start
				deps.Start = func(ctx context.Context, in NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
					if miss && in.Attempt == 1 {
						writePublishedFile(t, workspace, "vendor/module/file", "downloaded dependency", 0o644)
					}
					value, err := os.ReadFile(filepath.Join(workspace, "vendor/module/file"))
					if err != nil || string(value) != "downloaded dependency" {
						t.Fatalf("cache not retained for SDK work: %q %v", value, err)
					}
					return start(ctx, in, activity)
				}
				deps.BindStorage = func(_ context.Context, in NodeInput) (StorageBinding, error) {
					backend.upload.Identity.Attempt.Value = runspec.AttemptIdentity{Team: "team", RunID: in.Accepted.RunID, NodeID: in.NodeID, Generation: int64(in.Generation), Attempt: int64(in.Attempt)}
					backend.upload.Authority.Attempt = backend.upload.Identity.Attempt
					return StorageBinding{Bytes: backend, Records: backend, Authority: backend.upload.Authority}, nil
				}
				deps.PrepareLayer = func(ctx context.Context, layer LayerSpec, in NodeInput, job nodeproto.JobSpec) (ExecutorFinalizer, error) {
					finish, err := originalPrepare(ctx, layer, in, job)
					if layer.Name != "caches" || err != nil || miss {
						return finish, err
					}
					value, err := os.ReadFile(filepath.Join(workspace, "vendor/module/file"))
					if err != nil || string(value) != "downloaded dependency" {
						t.Fatalf("cache not restored before SDK work: %q %v", value, err)
					}
					return finish, nil
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
			if err := host.Execute(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			puts := 0
			if miss && behavior != "failed" {
				puts = 1
			}
			if len(processes) != 3 || backend.lookups != 1 || len(backend.puts) != puts {
				t.Fatalf("processes=%d lookups=%d puts=%d", len(processes), backend.lookups, len(backend.puts))
			}
			last := state.reports[len(state.reports)-1]
			if last.Extra.Result == nil || (last.Extra.Result.Outcome == store.NodeSuccess) != (behavior != "failed") {
				t.Fatalf("result=%+v", last)
			}
			for _, put := range backend.puts {
				if put.Identity.Attempt.Value.Attempt != 3 || put.Authority.Attempt.Value.Attempt != 3 {
					t.Fatal("cache save used an earlier attempt")
				}
			}
			for _, process := range processes {
				if !process.closed || !process.cleaned {
					t.Fatal("cache executor left SDK cleanup unjoined")
				}
			}
		})
	}
}

func depcacheFixture(t *testing.T) (NodeInput, DepcacheRecords, *depcacheBackendFixture, nodeproto.JobSpec) {
	t.Helper()
	input, _, publisher := artifactPublicationFixture(t)
	input.Entry = &source.Entry{Directory: t.TempDir()}
	publisher.upload.Key.Kind = blob.KindDepcache
	publisher.upload.Key.Name = "dependencies"
	backend := newDepcacheBackend(publisher)
	input.Storage = StorageBinding{Bytes: backend, Records: backend, Authority: backend.upload.Authority}
	adapter := DepcacheRecords{Bind: func(_ context.Context, _ NodeInput, key blob.DepcacheKey) (JobUpload, error) {
		if backend.mode == "refused_binding" {
			return JobUpload{}, outputRefused
		}
		backend.upload.Key.Scope.Key = key.Key
		backend.upload.Key.Name = key.Name
		return backend.upload, nil
	}}
	writePublishedFile(t, input.Entry.Directory, "lockfile", "dependencies-v1", 0o600)
	job := nodeproto.JobSpec{ID: input.NodeID, Caches: []nodeproto.CacheSpec{{Name: "dependencies", Path: "vendor", KeyFiles: []string{"lockfile"}}}}
	return input, adapter, backend, job
}

func TestDepcacheRecords(t *testing.T) {
	t.Helper()
	t.Run("miss_save_restore", func(t *testing.T) {
		input, adapter, backend, job := depcacheFixture(t)
		finish, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job)
		if err != nil || finish == nil {
			t.Fatalf("prepare=%v finalizer=%t", err, finish != nil)
		}
		writePublishedFile(t, input.Entry.Directory, "vendor/module/file", "downloaded dependency", 0o644)
		result := store.NodeResult{Outcome: store.NodeSuccess}
		if err := finish(t.Context(), input, &result); err != nil || backend.commit == nil || backend.commit.Key.Kind != blob.KindDepcache {
			t.Fatalf("save=%v commit=%+v", err, backend.commit)
		}
		if err := os.RemoveAll(filepath.Join(input.Entry.Directory, "vendor")); err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job); err != nil {
			t.Fatal(err)
		}
		value, err := os.ReadFile(filepath.Join(input.Entry.Directory, "vendor/module/file"))
		if err != nil || string(value) != "downloaded dependency" {
			t.Fatalf("restore=%q, %v", value, err)
		}
		if backend.lookup == nil || backend.lookup.Authority != input.Storage.Authority || backend.lookup.Key.Repo != input.Storage.Authority.Repo || backend.lookup.Key.Ref != input.Storage.Authority.Ref.Scope {
			t.Fatal("lookup lost its authenticated repository/ref scope")
		}
	})
	t.Run("expired_read_budget", func(t *testing.T) {
		input, adapter, backend, job := depcacheFixture(t)
		seedDepcache(t, input, adapter, backend, job)
		adapter.Budget.Deadline = time.Unix(1, 0)
		warnings := 0
		adapter.Warn = func(string) { warnings++ }
		if _, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(input.Entry.Directory, "vendor/module/file")); !errors.Is(err, os.ErrNotExist) || warnings == 0 {
			t.Fatal("expired cache read did not stay cold")
		}
	})
	for _, mode := range []string{"lookup", "missing_bytes", "forbidden", "corrupt", "unverified", "close", "gzip_crc", "bad_archive", "wrong_team", "wrong_repo", "wrong_key", "wrong_name", "wrong_kind"} {
		t.Run("cold_"+mode, func(t *testing.T) {
			input, adapter, backend, job := depcacheFixture(t)
			seedDepcache(t, input, adapter, backend, job)
			backend.mode = mode
			if mode == "gzip_crc" || mode == "bad_archive" {
				value := append([]byte{}, backend.values[backend.commit.Digest]...)
				if mode == "gzip_crc" {
					value[len(value)-8] ^= 1
				} else {
					value = []byte("invalid archive")
				}
				backend.commit.Digest = outputDigest(value)
				backend.values[backend.commit.Digest] = value
			}
			warnings := 0
			adapter.Warn = func(string) { warnings++ }
			if _, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job); err != nil {
				t.Fatal(err)
			}
			if warnings == 0 {
				t.Fatal("cache failure was silent")
			}
			if _, err := os.Stat(filepath.Join(input.Entry.Directory, "vendor/module/file")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed cache installed content: %v", err)
			}
			if backend.reader != nil && !backend.reader.closed {
				t.Fatal("failed cache left its reader open")
			}
			stages, err := filepath.Glob(filepath.Join(input.Entry.Directory, ".depcache-restore-*"))
			if err != nil || len(stages) != 0 {
				t.Fatalf("cache left staging files: %v %v", stages, err)
			}
		})
	}
	for _, mode := range []string{"put", "receipt", "commit", "record", "failed_job", "warm", "empty_directory", "changed_key", "missing_key", "refused_binding"} {
		t.Run("save_"+mode, func(t *testing.T) {
			input, adapter, backend, job := depcacheFixture(t)
			backend.mode = mode
			if mode == "missing_key" {
				if err := os.Remove(filepath.Join(input.Entry.Directory, "lockfile")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "warm" {
				writePublishedFile(t, input.Entry.Directory, "vendor/old", "warm", 0o600)
			}
			warnings := 0
			adapter.Warn = func(string) { warnings++ }
			finish, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job)
			if err != nil || finish == nil {
				t.Fatalf("prepare=%v", err)
			}
			if mode == "empty_directory" {
				if err := os.Mkdir(filepath.Join(input.Entry.Directory, "vendor"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				writePublishedFile(t, input.Entry.Directory, "vendor/module/file", "downloaded dependency", 0o644)
			}
			if mode == "changed_key" {
				writePublishedFile(t, input.Entry.Directory, "lockfile", "changed during execution", 0o600)
			}
			result := store.NodeResult{Outcome: store.NodeSuccess}
			if mode == "failed_job" {
				result.Outcome = store.NodeFailed
			}
			if err := finish(t.Context(), input, &result); err != nil || (result.Outcome == store.NodeSuccess) != (mode != "failed_job") {
				t.Fatalf("cache save changed the job verdict: %v %+v", err, result)
			}
			if mode == "changed_key" && (backend.commit == nil || backend.commit.Key.Scope.Key != backend.lookup.Key.Key) {
				t.Fatal("save rekeyed bytes after execution")
			}
			if mode == "warm" || mode == "failed_job" || mode == "missing_key" || mode == "refused_binding" || mode == "empty_directory" {
				if len(backend.puts) != 0 || backend.commit != nil {
					t.Fatal("saved disabled or failed cache")
				}
				if (mode == "missing_key" || mode == "refused_binding") && warnings == 0 {
					t.Fatal("disabled cache was silent")
				}
			} else if mode != "changed_key" && warnings == 0 {
				t.Fatal("cache save failure was silent")
			}
		})
	}
}

func seedDepcache(t *testing.T, input NodeInput, adapter DepcacheRecords, backend *depcacheBackendFixture, job nodeproto.JobSpec) {
	t.Helper()
	finish, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job)
	if err != nil || finish == nil {
		t.Fatalf("prepare seed=%v", err)
	}
	writePublishedFile(t, input.Entry.Directory, "vendor/module/file", "downloaded dependency", 0o644)
	result := store.NodeResult{Outcome: store.NodeSuccess}
	if err := finish(t.Context(), input, &result); err != nil || backend.commit == nil {
		t.Fatalf("seed=%v commit=%+v", err, backend.commit)
	}
	if err := os.RemoveAll(filepath.Join(input.Entry.Directory, "vendor")); err != nil {
		t.Fatal(err)
	}
}

func checkDepcacheConfinement(t *testing.T, name, link string, symlink bool) {
	t.Helper()
	input, adapter, backend, job := depcacheFixture(t)
	seedDepcache(t, input, adapter, backend, job)
	outside := t.TempDir()
	writePublishedFile(t, outside, "victim", "original", 0o600)
	name = strings.ReplaceAll(name, "$OUTSIDE", filepath.ToSlash(outside))
	link = strings.ReplaceAll(link, "$OUTSIDE", filepath.ToSlash(outside))
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	header := &tar.Header{Name: name, Mode: 0o644, Typeflag: tar.TypeReg, Size: 5}
	if symlink {
		header.Typeflag, header.Linkname, header.Size = tar.TypeSymlink, link, 0
	}
	if err := tw.WriteHeader(header); err != nil {
		return
	}
	if !symlink {
		if _, err := tw.Write([]byte("PWNED")); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := tw.WriteHeader(&tar.Header{Name: name + "/victim", Mode: 0o644, Typeflag: tar.TypeReg, Size: 5}); err == nil {
			if _, err := tw.Write([]byte("PWNED")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := errors.Join(tw.Close(), gz.Close()); err != nil {
		return
	}
	backend.commit.Digest = outputDigest(archive.Bytes())
	backend.values[backend.commit.Digest] = archive.Bytes()
	adapter.Warn = func(string) {}
	if _, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(outside, "victim"))
	if err != nil || string(value) != "original" {
		t.Fatalf("archive wrote outside cache: %q, %v; %s", value, err, fmt.Sprint(symlink))
	}
}

func checkDepcacheCaptureConfinement(t *testing.T, spelling string, keyfile bool) {
	t.Helper()
	input, adapter, backend, job := depcacheFixture(t)
	workspace, outside := input.Entry.Directory, t.TempDir()
	warnings := 0
	adapter.Warn = func(message string) {
		warnings++
		if strings.Contains(message, outside) || strings.Contains(message, "private-cache-content-outside-root") {
			t.Fatal("cache warning exposed outside data")
		}
	}
	var finish ExecutorFinalizer
	var err error
	if !keyfile {
		finish, err = adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job)
		if err != nil || finish == nil {
			t.Fatalf("prepare=%v", err)
		}
	}
	private := "private-cache-content-outside-root"
	writePublishedFile(t, outside, "private", private, 0o600)
	dir := filepath.Join(workspace, "vendor")
	if keyfile {
		dir = workspace
		job.Caches[0].KeyFiles = []string{"linked-key"}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "private")
	if len(spelling)%2 == 1 {
		var err error
		target, err = filepath.Rel(dir, target)
		if err != nil {
			t.Fatal(err)
		}
		target = strings.Repeat("."+string(filepath.Separator), len(spelling)%8) + target
	}
	for hop := 0; hop < len(spelling)%8; hop++ {
		name := fmt.Sprintf("hop-%d", hop)
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		target = name
	}
	name := "linked-file"
	if keyfile {
		name = "linked-key"
	}
	if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if keyfile {
		finish, err = adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job)
		if err != nil || finish == nil {
			t.Fatalf("prepare=%v", err)
		}
	}
	result := store.NodeResult{Outcome: store.NodeSuccess}
	if err := finish(t.Context(), input, &result); err != nil {
		t.Fatal(err)
	}
	if warnings == 0 || backend.commit != nil || len(backend.puts) != 0 || keyfile && backend.lookup != nil {
		t.Fatal("outside cache input reached storage")
	}
}

func checkDepcacheUploadBinding(t *testing.T, field uint8, value string) {
	t.Helper()
	input, adapter, backend, job := depcacheFixture(t)
	bind := adapter.Bind
	var expected, actual JobUpload
	adapter.Bind = func(ctx context.Context, current NodeInput, key blob.DepcacheKey) (JobUpload, error) {
		upload, err := bind(ctx, current, key)
		expected = upload
		switch field % 12 {
		case 0:
			upload.Key.Scope.Key = value
		case 1:
			upload.Key.Name = value
		case 2:
			upload.Key.Scope.Ref.Ref = value
		case 3:
			upload.Key.Scope.Repo = runspec.RepoKey(value)
		case 4:
			upload.Key.Team = value
		case 5:
			upload.Key.Scope.Job = value
		case 6:
			upload.Identity.Attempt.Value.Attempt = int64(len(value))
		case 7:
			upload.Authority.CapabilityRef = value
		case 8:
			upload.Authority.Principal = blob.Principal(value)
		case 9:
			upload.Identity.Kind = blob.UploadIdentityKind(value)
		case 10:
			upload.Identity.Attempt.Value.Generation = int64(len(value))
		case 11:
			upload.Key.Scope.Pipeline = value
		}
		actual = upload
		return upload, err
	}
	warnings := 0
	adapter.Warn = func(string) { warnings++ }
	finish, err := adapter.Prepare(t.Context(), LayerSpec{Name: "caches"}, input, job)
	if err != nil || finish == nil {
		t.Fatalf("prepare=%v", err)
	}
	writePublishedFile(t, input.Entry.Directory, "vendor/module/file", "dependency", 0o600)
	result := store.NodeResult{Outcome: store.NodeSuccess}
	if err := finish(t.Context(), input, &result); err != nil {
		t.Fatal(err)
	}
	if actual == expected {
		if backend.commit == nil || warnings != 0 {
			t.Fatal("matching cache binding failed")
		}
	} else if backend.commit != nil || len(backend.puts) != 0 || warnings == 0 {
		t.Fatal("mismatched cache binding reached storage")
	}
}
