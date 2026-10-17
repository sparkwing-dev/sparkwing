//go:build !windows

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestReleasedEngineOfflineUsesSignedPolicyAndVerifiedCache(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a signed engine and an exact-version SDK module fixture")
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	const version = "v1.2.3"
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("SPARKWING_HOME", t.TempDir())
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := releaseauth.TrustedPublicKeys
	releaseauth.TrustedPublicKeys = []string{base64.StdEncoding.EncodeToString(public)}
	t.Cleanup(func() { releaseauth.TrustedPublicKeys = previous })
	binary := filepath.Join(t.TempDir(), "engine")
	build := exec.Command("go", "build", "-p", "4", "-ldflags", "-X main.Version="+version, "-o", binary, "./cmd/sparkwing")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build released engine: %v\n%s", err, out)
	}
	asset, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	name, err := target.NameForVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	names, err := releaseasset.NamesForVersion(version)
	if err != nil {
		t.Fatal(err)
	}
	var manifest strings.Builder
	for _, entry := range names {
		body := []byte(entry)
		if entry == name {
			body = asset
		}
		fmt.Fprintf(&manifest, "%x  %s\n", sha256.Sum256(body), entry)
	}
	now := time.Now().UTC().Add(-time.Minute)
	policy, err := json.Marshal(engine.VersionIndex{Format: 1, PublishedAt: now, Latest: version, Versions: []engine.VersionInfo{{Version: version, PublishedAt: now, SupportUntil: now.AddDate(1, 0, 0)}}})
	if err != nil {
		t.Fatal(err)
	}
	base := engine.GitHubReleaseBase + "/" + version + "/"
	responses := map[string][]byte{
		engine.VersionIndexURL: policy, engine.VersionIndexSignatureURL: ed25519.Sign(key, policy),
		base + "SHA256SUMS": []byte(manifest.String()), base + "SHA256SUMS.sig": ed25519.Sign(key, []byte(manifest.String())),
		base + name: asset, base + name + ".sig": ed25519.Sign(key, asset),
	}
	downloads := 0
	online := &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
		body, ok := responses[r.URL.String()]
		if !ok {
			return nil, fmt.Errorf("unexpected download: %s", r.URL)
		}
		downloads++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	cache := t.TempDir()
	resolver, err := engine.NewResolver(engine.Config{CacheDirectory: cache, BuildVersion: version, HTTPClient: online})
	if err != nil {
		t.Fatal(err)
	}
	repo := initSnapshotRepo(t)
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	proxy := releasedSDKProxy(t, root, version)
	writeFile(t, filepath.Join(dir, "go.mod"), "module releasedfixture\n\ngo 1.26\n\nrequire "+engine.GoModule+" "+version+"\n")
	writeFile(t, filepath.Join(dir, "main.go"), `package main
import (
 "context"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
type pipeline struct{sw.Base}
func (pipeline) Plan(_ context.Context,p *sw.Plan,_ sw.NoInputs,_ sw.RunContext)error{
 sw.Job(p,"verify",func(context.Context)error{return nil});return nil
}
func main(){sw.Register("offline",func()sw.Pipeline[sw.NoInputs]{return pipeline{}});sw.Main()}
`)
	hostProxy := offlineHostModuleProxy(t)
	modCache := t.TempDir()
	offlineUnlockModuleCache(t, modCache)
	t.Setenv("GOMODCACHE", modCache)
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy)+",file://"+filepath.ToSlash(hostProxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("warm released SDK: %v\n%s", err, out)
	}
	options := source.Options{SDK: engine.GoSDK, Engines: resolver, Target: target, Base: procenv.Base{Local: true, Captured: os.Environ(), Masker: secrets.NewMasker()}}
	spec := source.Spec{RepoKey: "local:released-fixture", LocalCheckout: repo, PipelineDir: ".sparkwing"}
	selected := inspectReleasedOffline(t, spec, options)
	if selected.Identity.Pin.Replacement != "" || selected.Identity.Version != version || downloads != 6 {
		t.Fatalf("released resolution bypassed verification: %+v downloads=%d", selected.Identity, downloads)
	}
	attempts := 0
	offline := &http.Client{Transport: logRoundTrip(func(*http.Request) (*http.Response, error) { attempts++; return nil, fmt.Errorf("network denied") })}
	resolver, err = engine.NewResolver(engine.Config{CacheDirectory: cache, BuildVersion: version, HTTPClient: offline, LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOPROXY", "off")
	options.Engines = resolver
	options.Base.Captured = os.Environ()
	again := inspectReleasedOffline(t, spec, options)
	if again.Identity != selected.Identity || attempts != 0 {
		t.Fatalf("offline selection changed or used network: %+v attempts=%d", again.Identity, attempts)
	}
	policyResult, err := resolver.Index(t.Context(), now)
	if err != nil || policyResult.Index.Policy().Latest != version {
		t.Fatalf("offline run did not read signed policy: %+v: %v", policyResult, err)
	}
	if err := os.WriteFile(selected.Executable, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if entry, err := source.Acquire(t.Context(), spec, options); err == nil {
		if entry != nil {
			if closeErr := entry.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		}
		t.Fatal("corrupted released engine was accepted offline")
	}
}

func inspectReleasedOffline(t *testing.T, spec source.Spec, options source.Options) engine.Resolved {
	t.Helper()
	entry, err := source.Acquire(t.Context(), spec, options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	}()
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshots.Capture(t.Context(), options.Base.Captured)
	if err != nil {
		t.Fatal(err)
	}
	sessions := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: options.Base, Snapshots: snapshots}}
	var logs bytes.Buffer
	planner := nodehost.SDKPlanner{Log: func(_ context.Context, frame nodeproto.FrameLog) error {
		_, err := fmt.Fprintln(&logs, frame.Message)
		return err
	}, Start: sessions.StartPlanning, EnsureEngine: options.Engines.Ensure, Masker: options.Base.Masker}
	planned, err := planner.Prepare(t.Context(), nodehost.PlanningProcessInput{Entry: entry, Environment: snapshot, Entrypoint: "offline", Args: map[string]string{}, Run: nodeproto.RunContext{RunID: "released-offline", Pipeline: "offline"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}, nodehost.PlanningInput{})
	if err != nil || planned.Description.Name != "offline" {
		t.Fatalf("released SDK inspection: %+v: %v", planned.Description, err)
	}
	return entry.Engine
}

func releasedSDKProxy(t *testing.T, root, version string) string {
	t.Helper()
	stage := t.TempDir()
	list := exec.Command("go", "list", "-deps", "-json", "./sparkwing")
	list.Dir = root
	output, err := list.Output()
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for decoder.More() {
		var pkg struct {
			Dir                           string
			Module                        *struct{ Path string }
			GoFiles, CgoFiles, EmbedFiles []string
		}
		if err := decoder.Decode(&pkg); err != nil {
			t.Fatal(err)
		}
		if pkg.Module == nil || pkg.Module.Path != engine.GoModule {
			continue
		}
		for _, file := range append(append(pkg.GoFiles, pkg.CgoFiles...), pkg.EmbedFiles...) {
			input := filepath.Join(pkg.Dir, file)
			relative, err := filepath.Rel(root, input)
			if err != nil || strings.HasPrefix(relative, "..") {
				t.Fatalf("SDK fixture escaped root: %s: %v", input, err)
			}
			body, err := os.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(stage, relative)
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	body, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(stage, "go.mod"), string(body))
	proxy := t.TempDir()
	versions := filepath.Join(proxy, engine.GoModule, "@v")
	if err := os.MkdirAll(versions, 0o700); err != nil {
		t.Fatal(err)
	}
	archive, err := os.Create(filepath.Join(versions, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zipErr := modzip.CreateFromDir(archive, module.Version{Path: engine.GoModule, Version: version}, stage)
	closeErr := archive.Close()
	if zipErr != nil || closeErr != nil {
		t.Fatalf("SDK release zip: %v %v", zipErr, closeErr)
	}
	writeFile(t, filepath.Join(versions, version+".mod"), string(body))
	writeFile(t, filepath.Join(versions, version+".info"), `{"Version":"`+version+`","Time":"2026-10-14T00:00:00Z"}`)
	writeFile(t, filepath.Join(versions, "list"), version+"\n")
	return proxy
}
