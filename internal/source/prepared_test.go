package source_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

type preparedStorage struct {
	blob.DataSigner
	blob.RecordStore
	intent  blob.UploadIntent
	body    []byte
	record  blob.Record
	missing bool
	corrupt bool
	denied  error
}

func (s *preparedStorage) Upload(_ context.Context, in blob.UploadIntent) (blob.UploadTicket, error) {
	s.intent = in
	return blob.UploadTicket{ID: "upload", Transfer: blob.SignedTransfer{Digest: in.Digest, Size: in.Size}}, nil
}
func (s *preparedStorage) Commit(_ context.Context, _ blob.CommitUpload) (blob.Receipt, error) {
	return blob.Receipt{Identity: s.intent.Identity, Digest: s.intent.Digest}, nil
}
func (s *preparedStorage) Download(_ context.Context, req blob.GetRequest) (blob.SignedTransfer, error) {
	if s.denied != nil {
		return blob.SignedTransfer{}, s.denied
	}
	if s.missing {
		return blob.SignedTransfer{}, blob.ErrNotFound
	}
	return blob.SignedTransfer{Digest: req.Digest, Size: int64(len(s.body))}, nil
}
func (s *preparedStorage) Transfer(_ context.Context, _ blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
	if body != nil {
		var err error
		s.body, err = io.ReadAll(body)
		return io.NopCloser(bytes.NewReader(nil)), err
	}
	data := bytes.Clone(s.body)
	if s.corrupt {
		data[len(data)/2] ^= 1
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (s *preparedStorage) CommitRecord(_ context.Context, req blob.CommitRecordRequest) (blob.Record, error) {
	s.record = blob.Record{Key: req.Key, Digest: req.Digest}
	return s.record, nil
}
func (s *preparedStorage) LookupRecord(_ context.Context, _ blob.RecordLookup) (blob.RecordLookupResult, error) {
	return blob.RecordLookupResult{Found: s.record.Digest != "", Record: s.record}, nil
}

type preparedFixture struct {
	spec     source.Spec
	options  source.Options
	storage  *preparedStorage
	installs int
	resolver *engineResolver
}

func newPreparedFixture(t *testing.T, sdk engineid.SDKKind) *preparedFixture {
	t.Helper()
	dir, sha := sourcetest.Repository(t)
	options, resolver, _ := acquisitionOptions(t, filepath.Join(dir, ".sparkwing"))
	resolver.resolved.Identity.Pin.SDK = sdk
	f := &preparedFixture{spec: source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir, SHA: sha}, options: options, resolver: resolver, storage: &preparedStorage{}}
	f.options.Workspace = t.TempDir()
	f.options.SDK = sdk
	f.options.Base = procenv.Base{Local: true, Captured: os.Environ()}
	f.options.ReadPin = func(_ string, _ engineid.SDKKind) (engineid.Pin, error) { return f.resolver.resolved.Identity.Pin, nil }
	f.options.InstallSDK = func(_ context.Context, _ engine.Resolved, req engine.SDKInstallRequest) (engine.SDKCommand, error) {
		f.installs++
		directory := filepath.Join(req.Directory, req.PipelineDir)
		command := filepath.Join(t.TempDir(), "entry")
		if sdk == engineid.TypeScriptSDK {
			module := filepath.Join(directory, "node_modules", "prepared-dependency")
			if err := os.MkdirAll(module, 0o700); err != nil {
				t.Fatal(err)
			}
			sourcetest.Write(t, filepath.Join(module, "index.js"), "module.exports='prepared execution';\n")
			command = filepath.Join(directory, "main.cjs")
			sourcetest.Write(t, command, "console.log(require('prepared-dependency'));\n")
			node, err := exec.LookPath("node")
			if err != nil {
				t.Fatal(err)
			}
			return engine.SDKCommand{Directory: directory, Command: []string{node, command}}, nil
		}
		if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf 'prepared execution\\n'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return engine.SDKCommand{Directory: directory, Command: []string{command}}, nil
	}
	identity := blob.UploadIdentity{Kind: blob.UploadOwnerUnit, Team: "team", RunID: "run", Principal: "owner", CapabilityRef: "cap", Unit: runspec.Optional[blob.UploadUnit]{Set: true, Value: blob.UploadUnit{Kind: blob.UploadPlanUnit, ID: "run", Generation: 1}}}
	f.options.Prepared = &source.Prepared{Bytes: blob.NewSignedStore(blob.SignedStoreOptions{Signer: f.storage, HTTPClient: f.storage}), Records: f.storage, Authority: blob.Authority{Upload: runspec.Optional[blob.UploadIdentity]{Set: true, Value: identity}}, Key: blob.PreparedRecordKey("team", "run", runspec.RepoKey(f.spec.RepoKey), "pipeline"), RequestHash: "sealed-request", Publish: true}
	return f
}

func (f *preparedFixture) publish(t *testing.T) {
	t.Helper()
	entry, err := source.Acquire(t.Context(), f.spec, f.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
	if len(f.storage.body) == 0 || f.storage.record.Digest == "" {
		t.Fatal("planning did not publish prepared bytes and record")
	}
	f.spec.Engine = &f.resolver.resolved.Identity
	f.options.Prepared.Publish = false
}

func assertPreparedExecution(t *testing.T, entry *source.Entry) {
	t.Helper()
	command := exec.CommandContext(t.Context(), entry.Command[0], entry.Command[1:]...)
	command.Dir = entry.Directory
	output, err := command.CombinedOutput()
	if err != nil || string(output) != "prepared execution\n" {
		t.Fatalf("restored command output=%q err=%v", output, err)
	}
}

func TestAcquirePreparedRestoresExecutableWithoutSourceOrInstaller(t *testing.T) {
	for _, sdk := range []engineid.SDKKind{engineid.GoSDK, engineid.TypeScriptSDK} {
		t.Run(string(sdk), func(t *testing.T) {
			f := newPreparedFixture(t, sdk)
			f.publish(t)
			if sdk == engineid.TypeScriptSDK {
				f.options.SDK = ""
			}
			if err := os.RemoveAll(f.spec.LocalCheckout); err != nil {
				t.Fatal(err)
			}
			f.options.InstallSDK = func(context.Context, engine.Resolved, engine.SDKInstallRequest) (engine.SDKCommand, error) {
				t.Fatal("restoration ran installer")
				return engine.SDKCommand{}, nil
			}
			entry, err := source.Acquire(t.Context(), f.spec, f.options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := entry.Close(); err != nil {
					t.Error(err)
				}
			}()
			if entry.SHA != f.spec.SHA || entry.CheckoutDirectory() == f.spec.LocalCheckout || f.installs != 1 {
				t.Fatalf("restored entry=%+v installs=%d", entry, f.installs)
			}
			assertPreparedExecution(t, entry)
		})
	}
}

func TestAcquirePreparedFallsBackForDifferentInputsAndAbsentBytes(t *testing.T) {
	for _, change := range []string{"environment", "source", "engine", "request", "missing object", "missing record"} {
		t.Run(change, func(t *testing.T) {
			f := newPreparedFixture(t, engineid.GoSDK)
			f.publish(t)
			switch change {
			case "environment":
				f.options.Base.Captured = append(f.options.Base.Captured, "HOME="+t.TempDir())
			case "source":
				f.spec.HashAllFiles = true
			case "engine":
				identity := f.resolver.resolved.Identity
				identity.ManifestDigest = "another-engine"
				f.spec.Engine = &identity
				f.resolver.resolved.Identity = identity
			case "request":
				f.options.Prepared.RequestHash = "another-request"
			case "missing object":
				f.storage.missing = true
			case "missing record":
				f.storage.record = blob.Record{}
			}
			entry, err := source.Acquire(t.Context(), f.spec, f.options)
			if err != nil {
				t.Fatal(err)
			}
			if err := entry.Close(); err != nil {
				t.Fatal(err)
			}
			if f.installs != 2 {
				t.Fatalf("changed %s reused preparation: installs=%d", change, f.installs)
			}
		})
	}
}

func TestAcquirePreparedRejectsCorruptOrUnauthorizedBytesBeforeInstall(t *testing.T) {
	for _, failure := range []string{"corrupt", "authority", "record key"} {
		t.Run(failure, func(t *testing.T) {
			f := newPreparedFixture(t, engineid.GoSDK)
			f.publish(t)
			want := blob.ErrIntegrity
			switch failure {
			case "corrupt":
				f.storage.corrupt = true
			case "authority":
				want = blob.ErrDigestNotReadable
				f.storage.denied = want
			case "record key":
				f.storage.record.Key.Scope.Key = "other-run"
			}
			f.resolver.calls = nil
			entry, err := source.Acquire(t.Context(), f.spec, f.options)
			if !errors.Is(err, want) || entry != nil || f.installs != 1 || len(f.resolver.calls) != 0 {
				t.Fatalf("entry=%v err=%v installs=%d engine calls=%v", entry, err, f.installs, f.resolver.calls)
			}
		})
	}
}

func TestAcquirePreparedPreservesSeparatePipelineSource(t *testing.T) {
	f := newPreparedFixture(t, engineid.GoSDK)
	pipelineSHA := f.spec.SHA
	sourcetest.Write(t, filepath.Join(f.spec.LocalCheckout, "source.txt"), "new source\n")
	sourcetest.Git(t, f.spec.LocalCheckout, "add", ".")
	sourcetest.Git(t, f.spec.LocalCheckout, "commit", "-qm", "source revision")
	f.spec.SHA = sourcetest.Git(t, f.spec.LocalCheckout, "rev-parse", "HEAD")
	f.spec.PipelineSHA = pipelineSHA
	f.publish(t)
	if err := os.RemoveAll(f.spec.LocalCheckout); err != nil {
		t.Fatal(err)
	}
	entry, err := source.Acquire(t.Context(), f.spec, f.options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	}()
	data, err := os.ReadFile(filepath.Join(entry.CheckoutDirectory(), "source.txt"))
	if err != nil || string(data) != "new source\n" {
		t.Fatalf("source revision: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(entry.Directory, "..", "source.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pipeline source contains later source revision: %v", err)
	}
	assertPreparedExecution(t, entry)
}

func TestAcquirePreparedRestoresAfterGitCredentialRevocation(t *testing.T) {
	f := newPreparedFixture(t, engineid.GoSDK)
	original := f.spec.LocalCheckout
	const remote = "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: original})
	f.spec.LocalCheckout = ""
	f.spec.RepoKey = "git:8.8.8.8/team/repo"
	f.spec.RemoteURL = remote
	f.options.Base = procenv.Base{Ambient: sourcetest.Environment(t), Masker: secrets.NewMasker()}
	authority := &access{credential: source.Credential{Kind: "https", Host: "8.8.8.8", Username: "source", Secret: "private-source-password"}}
	f.options.Access = authority
	f.publish(t)
	if authority.calls != 1 {
		t.Fatalf("planning credential calls=%d", authority.calls)
	}
	authority.err = errors.New("source credential revoked")
	if err := os.RemoveAll(original); err != nil {
		t.Fatal(err)
	}
	entry, err := source.Acquire(t.Context(), f.spec, f.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := entry.Close(); err != nil {
		t.Fatal(err)
	}
	if authority.calls != 1 || f.installs != 1 {
		t.Fatalf("restoration requested source: credentials=%d installs=%d", authority.calls, f.installs)
	}
}

func TestAcquirePreparedRealGoBuildTiming(t *testing.T) {
	f := newPreparedFixture(t, engineid.GoSDK)
	sourcetest.Write(t, filepath.Join(f.spec.LocalCheckout, ".sparkwing", "pipeline.go"), "package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"prepared execution\") }\n")
	f.options.InstallSDK = func(ctx context.Context, _ engine.Resolved, req engine.SDKInstallRequest) (engine.SDKCommand, error) {
		f.installs++
		directory := filepath.Join(req.Directory, req.PipelineDir)
		executable := filepath.Join(t.TempDir(), "pipeline")
		build := exec.CommandContext(ctx, "go", "build", "-o", executable, "pipeline.go")
		build.Dir, build.Env = directory, req.Environment
		if output, err := build.CombinedOutput(); err != nil {
			return engine.SDKCommand{}, fmt.Errorf("build fixture: %w: %s", err, output)
		}
		return engine.SDKCommand{Directory: directory, Command: []string{executable}}, nil
	}
	started := time.Now()
	f.publish(t)
	fresh := time.Since(started)
	if err := os.RemoveAll(f.spec.LocalCheckout); err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	restored, err := source.Acquire(t.Context(), f.spec, f.options)
	restore := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restored.Close(); err != nil {
			t.Error(err)
		}
	}()
	assertPreparedExecution(t, restored)
	if f.installs != 1 {
		t.Fatalf("installs=%d, want one real Go build", f.installs)
	}
	t.Logf("real Go fixture, shared warm build cache: fresh Acquire+publication=%s; restored Acquire=%s; installs=%d", fresh, restore, f.installs)
}

func TestAcquirePreparedRestoresPlanningOwnerAfterRestart(t *testing.T) {
	f := newPreparedFixture(t, engineid.GoSDK)
	f.publish(t)
	published := bytes.Clone(f.storage.body)
	record := f.storage.record
	f.spec.Engine = nil
	f.options.Prepared.Publish = true
	if err := os.RemoveAll(f.spec.LocalCheckout); err != nil {
		t.Fatal(err)
	}
	entry, err := source.Acquire(t.Context(), f.spec, f.options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	}()
	if f.installs != 1 || f.storage.record != record || !bytes.Equal(f.storage.body, published) {
		t.Fatalf("planner restart rebuilt or republished: installs=%d record=%+v", f.installs, f.storage.record)
	}
	assertPreparedExecution(t, entry)
}
