package main

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
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

type uploadedBundle []byte

func (b uploadedBundle) BundleURL(context.Context, string) (string, error) {
	return "https://upload.example/source", nil
}

func (b uploadedBundle) GitCredential(context.Context, source.Spec) (source.Credential, error) {
	return source.Credential{}, errors.New("uploaded source must not fetch Git")
}

func (b uploadedBundle) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
}

func TestUploadedGoPipelinePlansThroughRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles the Go pipeline")
	}
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("SPARKWING_HOME", filepath.Join(root, "state"))
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-buildvcs=false")
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(root, "upload")
	pipeline := filepath.Join(upload, ".sparkwing")
	if err := os.MkdirAll(pipeline, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(pipeline, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module uploaded-pipeline\ngo 1.26\nrequire github.com/sparkwing-dev/sparkwing v0.68.2\nreplace github.com/sparkwing-dev/sparkwing => "+filepath.ToSlash(repo)+"\n")
	write("main.go", `package main
import("context"; "github.com/sparkwing-dev/sparkwing/sparkwing")
type pipeline struct { sparkwing.Base }
type inputs struct{}
func (pipeline) ShortHelp() string {return "Uploaded Go gate"}
func (pipeline) Plan(ctx context.Context, p *sparkwing.Plan, _ inputs, _ sparkwing.RunContext) error {
sparkwing.Job(p,"build",func(context.Context) error {return nil}); return nil
}
func main(){sparkwing.Register[inputs]("gate",func() sparkwing.Pipeline[inputs]{return pipeline{}});sparkwing.Main()}
`)
	git := func(args ...string) string {
		t.Helper()
		c := testgit.CommandContext(t.Context(), "git", append([]string{"-C", upload}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	git("init", "--quiet", "--initial-branch=main")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "uploaded pipeline")
	sha := strings.TrimSpace(git("rev-parse", "HEAD"))
	bundle := filepath.Join(root, "source.bundle")
	git("bundle", "create", bundle, "HEAD")
	body, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	resolver, err := engine.NewResolver(engine.Config{CacheDirectory: filepath.Join(root, "engines")})
	if err != nil {
		t.Fatal(err)
	}
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	base := procenv.Base{Local: true, Captured: testgit.Env(os.Environ())}
	entry, err := source.Acquire(t.Context(), source.Spec{RepoKey: "git:example.test/team/repo", RemoteURL: "https://example.test/team/repo", SHA: sha, BundleDigest: hex.EncodeToString(digest[:])}, source.Options{Access: uploadedBundle(body), BundleTransport: uploadedBundle(body), Workspace: filepath.Join(root, "workspaces"), Engines: resolver, Target: target, Base: base})
	if err != nil {
		t.Fatalf("uploaded Go pipeline acquisition through sparkwing-runner: %v", err)
	}
	defer func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	}()
	config := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{}}
	planner := nodehost.SDKPlanner{Start: config.StartPlanning, EnsureEngine: resolver.Ensure, Masker: secrets.NewMasker(), Log: func(context.Context, nodeproto.FrameLog) error { return nil }}
	prepared, err := planner.Prepare(t.Context(), nodehost.PlanningProcessInput{Entry: entry, Runner: nodeproto.RunnerIdentity{Name: "cloud", Type: "cloud", Labels: []string{}}, Entrypoint: "gate", Run: nodeproto.RunContext{RunID: "uploaded-run", Pipeline: "gate"}}, nodehost.PlanningInput{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := prepared.Plan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var plan nodeproto.PlanDoc
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Jobs) != 1 || plan.Jobs[0].ID != "build" {
		t.Fatalf("uploaded gate plan: %s", raw)
	}
}
