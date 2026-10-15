package nodehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

const (
	sourceRemote    = "https://8.8.8.8/team/repo"
	sourceBundleURL = "https://bundles.example.test/seed?signature=private-bundle-signature"
	sourceSecret    = "private-source-password"
)

type sourceAdapters struct {
	resolved engine.Resolved
	bundle   []byte
	digest   string
	tree     string
	trace    []string
	closed   int
}

func (a *sourceAdapters) GitCredential(_ context.Context, spec source.Spec) (source.Credential, error) {
	if spec.RemoteURL != sourceRemote {
		return source.Credential{}, errors.New("source release changed repository")
	}
	a.trace = append(a.trace, "authorize")
	return source.Credential{Kind: "https", Host: "8.8.8.8", Username: "fixture", Secret: sourceSecret}, nil
}

func (a *sourceAdapters) BundleURL(_ context.Context, digest string) (string, error) {
	if digest != a.digest {
		return "", errors.New("source bundle changed digest")
	}
	a.trace = append(a.trace, "sign-bundle")
	return sourceBundleURL, nil
}

func (a *sourceAdapters) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodGet || request.URL.String() != sourceBundleURL {
		return nil, errors.New("unexpected source transfer")
	}
	a.trace = append(a.trace, "download-bundle")
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: sourceBundleReader{Reader: bytes.NewReader(a.bundle), close: func() { a.closed++ }}}, nil
}

type sourceBundleReader struct {
	io.Reader
	close func()
}

func (r sourceBundleReader) Close() error { r.close(); return nil }

func (a *sourceAdapters) Resolve(context.Context, engine.Pin, releaseasset.Target) (engine.Resolved, error) {
	return engine.Resolved{}, errors.New("accepted source must restore its sealed engine")
}

func (a *sourceAdapters) Ensure(_ context.Context, identity engine.Identity, target releaseasset.Target) (engine.Resolved, error) {
	if identity != a.resolved.Identity || target != a.resolved.Target {
		return engine.Resolved{}, errors.New("source changed sealed engine")
	}
	a.trace = append(a.trace, "ensure")
	return a.resolved, nil
}

func (a *sourceAdapters) Install(_ context.Context, resolved engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
	directory, environment := filepath.Join(request.Directory, request.PipelineDir), request.Environment
	if !reflect.DeepEqual(resolved, a.resolved) || len(environment) != 2 {
		return engine.SDKCommand{}, errors.New("installer lost engine or explicit environment")
	}
	body, err := os.ReadFile(filepath.Join(directory, "pipeline.go"))
	if err != nil || string(body) != "package main // sealed\n" {
		return engine.SDKCommand{}, errors.New("installer did not receive the sealed source")
	}
	a.trace = append(a.trace, "install")
	return engine.SDKCommand{Directory: directory, Command: []string{filepath.Join(directory, "entry")}}, nil
}

func sourceAcquisitionFixture(t *testing.T) (runspec.Spec, source.Options, *sourceAdapters, string) {
	t.Helper()
	directory, _ := sourcetest.Repository(t)
	bundlePath := filepath.Join(t.TempDir(), "seed.bundle")
	sourcetest.Git(t, directory, "bundle", "create", bundlePath, "--all")
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	sourcetest.Write(t, filepath.Join(directory, ".sparkwing", "pipeline.go"), "package main // sealed\n")
	sourcetest.Git(t, directory, "add", ".")
	sourcetest.Git(t, directory, "commit", "-qm", "sealed")
	sealed := sourcetest.Git(t, directory, "rev-parse", "HEAD")
	tree := sourcetest.Git(t, directory, "rev-parse", "HEAD^{tree}")
	sourcetest.Write(t, filepath.Join(directory, ".sparkwing", "pipeline.go"), "package main // advanced\n")
	sourcetest.Git(t, directory, "add", ".")
	sourcetest.Git(t, directory, "commit", "-qm", "advanced")
	sourcetest.Git(t, directory, "branch", "-m", "future")
	ledger := sourcetest.Remote(t, map[string]string{sourceRemote: directory})
	digest := sha256.Sum256(bundle)
	a := &sourceAdapters{digest: hex.EncodeToString(digest[:]), tree: tree, bundle: bundle, resolved: engine.Resolved{
		Identity: engine.Identity{Version: "v1.2.3", ManifestDigest: "verified-release", Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}},
		Target:   releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"}, Executable: filepath.Join(t.TempDir(), "matched-engine"),
	}}
	spec := runspec.Spec{Request: runspec.SealedRequest{RunID: "original"}, Resolved: runspec.Resolved{Engine: a.resolved.Identity}}
	spec.Request.Request.Source.Location = runspec.SourceLocation{RemoteURL: sourceRemote, PipelineDir: ".sparkwing"}
	spec.Request.Request.Git.Repository.Key, spec.Request.Request.Git.SHA = "git:8.8.8.8/team/repo", sealed
	spec.Request.Request.Git.DefaultBranch.Set, spec.Request.Request.Git.DefaultBranch.Value = true, "trunk"
	options := source.Options{
		Workspace: t.TempDir(), Access: a, Engines: a, Target: a.resolved.Target, InstallSDK: a.Install,
		Base: procenv.Base{Local: true, Captured: sourcetest.Environment(t)}, BuildEnvironment: sourcetest.BuildEnvironment,
		ReadPin:         func(string, engine.SDKKind) (engine.Pin, error) { return a.resolved.Identity.Pin, nil },
		BundleTransport: a,
	}
	return spec, options, a, ledger
}

func TestDirectSource(t *testing.T) {
	t.Helper()
	spec, options, adapters, ledger := sourceAcquisitionFixture(t)
	for _, bundled := range []bool{false, true} {
		name := "direct"
		if bundled {
			name = "bundle-and-delta"
		}
		t.Run(name, func(t *testing.T) {
			sealed := spec
			if bundled {
				sealed.Request.Request.Source.BundleDigest.Set, sealed.Request.Request.Source.BundleDigest.Value = true, adapters.digest
			}
			for range 2 {
				adapters.trace = nil
				projected, err := source.ForExecution(sealed)
				if err != nil {
					t.Fatal(err)
				}
				entry, err := source.Acquire(t.Context(), projected, options)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := entry.Close(); err != nil {
						t.Error(err)
					}
				})
				if entry.SHA != spec.Request.Request.Git.SHA || entry.DefaultBranch != "trunk" || entry.RepoKey != string(spec.Request.Request.Git.Repository.Key) || len(entry.Command) != 1 {
					t.Fatalf("source changed sealed facts: %+v", entry)
				}
				if tree := sourcetest.Git(t, entry.Directory, "rev-parse", "HEAD^{tree}"); tree != adapters.tree {
					t.Fatalf("source tree = %s, want sealed tree %s", tree, adapters.tree)
				}
				want := []string{"authorize", "ensure", "install"}
				if bundled {
					want = []string{"authorize", "sign-bundle", "download-bundle", "ensure", "install"}
				}
				if !reflect.DeepEqual(adapters.trace, want) {
					t.Fatalf("source trace = %v, want %v", adapters.trace, want)
				}
				if err := entry.Close(); err != nil {
					t.Fatal(err)
				}
				remaining, err := os.ReadDir(options.Workspace)
				if err != nil || len(remaining) != 0 {
					t.Fatalf("source left owned files: %v, %v", remaining, err)
				}
			}
		})
	}
	if adapters.closed != 2 {
		t.Fatalf("bundle readers closed = %d, want 2", adapters.closed)
	}
	assertSourceCredentialCleanup(t, ledger)
}

func assertSourceCredentialCleanup(t *testing.T, ledger string) {
	t.Helper()
	data, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), sourceSecret) || strings.Contains(string(data), sourceBundleURL) {
		t.Fatal("source credentials entered git argv or environment")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var record struct {
			Files []string          `json:"files"`
			Env   map[string]string `json:"env"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		for _, name := range append(record.Files, filepath.Join(record.Env["HOME"], ".netrc")) {
			if _, err := os.Stat(name); !os.IsNotExist(err) {
				t.Fatalf("source credential file remains: %s, %v", name, err)
			}
		}
	}
}
