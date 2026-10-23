package nodehost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestExecutorArtifactPublication(t *testing.T) {
	t.Helper()
	for _, mode := range []string{"success", "put", "commit", "record", "body_failed", "result_refused"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			writePublishedFile(t, workspace, "dist/result", "compiled result", 0o755)
			_, adapter, backend := artifactPublicationFixture(t)
			backend.mode = mode
			var trace []string
			var processes []*scriptedSDK
			executionMode := "artifact_publish_retry"
			if mode == "body_failed" {
				executionMode = "artifact_publish_failed"
			}
			input, state, host := executionHostFixture(t, executionMode, &trace, &processes, func(deps *ExecutorDependencies) {
				deps.BindStorage = func(_ context.Context, in NodeInput) (StorageBinding, error) {
					backend.upload.Identity.Attempt.Value = runspec.AttemptIdentity{Team: "team", RunID: in.Accepted.RunID, NodeID: in.NodeID, Generation: int64(in.Generation), Attempt: int64(in.Attempt)}
					backend.upload.Authority.Attempt = backend.upload.Identity.Attempt
					backend.upload.Key = blob.ResultRecordKey(backend.upload.Identity.Attempt.Value, backend.upload.Authority.Repo, backend.upload.Authority.Ref.Scope, "build", blob.KindManifest)
					return StorageBinding{Bytes: backend, Records: backend, Authority: backend.upload.Authority}, nil
				}
				deps.PrepareArtifacts = adapter.Prepare
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
				state.reject = runstate.CauseReport
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
			if last.Cause != runstate.CauseReport || result == nil {
				t.Fatalf("report=%+v", last)
			}
			wantSuccess := mode == "success" || mode == "result_refused"
			if (result.Outcome == runstate.NodeSuccess) != wantSuccess || (result.ManifestRecord != nil) != wantSuccess {
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
		input := NodeInput{Entry: &source.Entry{Directory: t.TempDir()}}
		if _, err := a.Prepare(t.Context(), input, nodeproto.JobSpec{Artifacts: []string{"../outside"}}); err == nil {
			t.Fatal("accepted invalid capture path before execution")
		}
	})
}

func artifactPublicationFixture(t *testing.T) (NodeInput, ArtifactRecords, *artifactPublisherFixture) {
	t.Helper()
	input, _, backend, _ := outputFixture(t, "success")
	backend.upload.Key = blob.ResultRecordKey(backend.upload.Identity.Attempt.Value, backend.upload.Authority.Repo, backend.upload.Authority.Ref.Scope, "build", blob.KindManifest)
	p := newArtifactPublisher(backend.upload)
	a := ArtifactRecords{Blobs: p, Records: p, Bind: func(context.Context, NodeInput) (JobUpload, error) { return p.upload, nil }}
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

func TestArtifactPublication(t *testing.T) {
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
		input.Storage = StorageBinding{Bytes: p, Records: p, Authority: p.upload.Authority}
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
				input.Storage = StorageBinding{Bytes: p, Records: p, Authority: p.upload.Authority}
				input.Storage.Authority.CapabilityRef = "old-authority"
			case "partial_storage":
				input.Storage = StorageBinding{Bytes: p}
			case "bind":
				a.Bind = func(context.Context, NodeInput) (JobUpload, error) {
					return JobUpload{}, outputRefused
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
		t.Run("symlinks", func(t *testing.T) { checkArtifactCaptureConfinement(t, "../", literal) })
	}
}

func checkArtifactCaptureConfinement(t *testing.T, spelling string, literal bool) {
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

func checkArtifactUploadBinding(t *testing.T, field uint8, value string) {
	t.Helper()
	input, a, backend := artifactPublicationFixture(t)
	want := backend.upload
	actual := &backend.upload
	switch field % 16 {
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
