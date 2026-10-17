package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestTriggerInspectionUsesRetainedBuildPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and describes a real SDK pipeline")
	}
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "missing.yaml"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	repo := initSnapshotRepo(t)
	dir := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, dir, `package main
import (
 "context"
 _ "embed"
 "fmt"
 "net/url"
 "os"
 sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)
//go:embed payload
var payload string
type inputs struct { Region string `+"`flag:\"region\" required:\"true\"`"+`; Password string `+"`flag:\"password\" secret:\"true\"`"+` }
type pipeline struct{sw.Base}
func (pipeline) Help()string{
 proxy,_:=url.Parse(os.Getenv("HTTPS_PROXY"))
 if proxy!=nil&&proxy.User!=nil{value,_:=proxy.User.Password();fmt.Fprintln(os.Stderr,value)}
 return payload
}
func (pipeline) Plan(_ context.Context,p *sw.Plan,_ inputs,_ sw.RunContext)error{
 sw.Job(p,"verify",func(context.Context)error{return nil});return nil
}
func main(){sw.Register("ci",func()sw.Pipeline[inputs]{return pipeline{}});sw.Main()}
`)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: ci\n    entrypoint: ci\n", 0o600)
	writeSnapshotFile(t, repo, ".sparkwing/payload", "first", 0o600)
	writeSnapshotFile(t, repo, ".gitignore", ".sparkwing/payload\n", 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "-qm", "fixture")
	sha := strings.TrimSpace(runSnapshotGit(t, repo, "rev-parse", "HEAD"))
	repository, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: repo})
	if err != nil {
		t.Fatal(err)
	}
	selected := preparedTriggerSource{pipeline: "ci", git: runspec.Git{Repository: repository, SHA: sha}, source: runspec.Source{Ref: sha, Entrypoint: "ci", Location: runspec.SourceLocation{LocalCheckout: repo, PipelineDir: ".sparkwing"}}}
	for _, tc := range []struct{ flag, want string }{{"", "first"}, {"--sw-hash-all-files", "second"}, {"--sw-no-bincache", "third"}} {
		t.Run(tc.flag, func(t *testing.T) {
			writeSnapshotFile(t, repo, ".sparkwing/payload", tc.want, 0o600)
			args := []string{"run", "ci"}
			if tc.flag != "" {
				args = append(args, tc.flag)
			}
			inv, err := clisurface.Parse("sparkwing", args)
			if err != nil {
				t.Fatal(err)
			}
			flags, _ := runFlagsFromInvocation(inv)
			selected.source.NoBincache, selected.source.HashAllFiles = flags.noBincache, flags.hashAllFiles
			options, err := hostSourceOptions(home, os.Environ(), secrets.NewMasker(), true)
			if err != nil {
				t.Fatal(err)
			}
			described, err := inspectTriggerSource(t.Context(), selected, os.Environ(), options)
			if err != nil || len(described) != 1 || described[0].Name != "ci" || described[0].Help != tc.want {
				t.Fatalf("selected policy inspection: %+v: %v", described, err)
			}
			req, err := prepareTriggerRequest(triggerRequestInput{source: selected, described: described, args: []string{"--region=west", "--password=private-value"}, settings: settings.Inputs{Context: settings.Context{NumCPU: 4}}})
			if err != nil {
				t.Fatal(err)
			}
			if req.Request.RequestedArgs["region"] != "west" || len(req.Request.SecretArgs.Value) != 1 || req.Request.SecretArgs.Value[0] != "password" {
				t.Fatal("inspection lost arguments or secret classification")
			}
			req.Request.Team = "default"
			seal, err := runspec.SealRequest(req.Request, runspec.IntakeFacts{RunID: "policy"})
			if err != nil {
				t.Fatal(err)
			}
			retry, err := runspec.CarryRetry(runspec.RetryInput{Request: seal, SourceRunID: seal.RunID})
			if err != nil {
				t.Fatal(err)
			}
			spec, err := source.ForRequest(retry.Request)
			if err != nil {
				t.Fatal(err)
			}
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
			env, err := snapshots.Capture(t.Context(), os.Environ())
			if err != nil {
				t.Fatal(err)
			}
			sessions := nodehost.ProtocolSessionConfig{Environments: nodehost.SDKEnvironment{Base: options.Base, Snapshots: snapshots}}
			planner := nodehost.SDKPlanner{Start: sessions.StartPlanning, EnsureEngine: options.Engines.Ensure, Masker: options.Base.Masker, Log: func(_ context.Context, frame nodeproto.FrameLog) error { t.Log(frame.Message); return nil }}
			prepared, err := planner.Prepare(t.Context(), nodehost.PlanningProcessInput{Entry: entry, Entrypoint: "ci", Environment: env, Args: map[string]string{"region": "west"}, Run: nodeproto.RunContext{RunID: "retry", Pipeline: "ci"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}, nodehost.PlanningInput{})
			if err != nil || prepared.Description.Help != tc.want {
				t.Fatalf("retry changed build policy: %+v: %v", prepared.Description, err)
			}
		})
	}

	selected.source.NoBincache, selected.source.HashAllFiles = false, false
	property := func(raw []byte) bool {
		secret := "private-proxy-" + hex.EncodeToString(raw) + "-credential"
		captured := slices.DeleteFunc(slices.Clone(os.Environ()), func(item string) bool { return strings.HasPrefix(item, "HTTPS_PROXY=") })
		captured = append(captured, "HTTPS_PROXY=https://"+url.UserPassword("inspection", secret).String()+"@proxy.example")
		options, err := hostSourceOptions(home, captured, secrets.NewMasker(), true)
		if err != nil {
			t.Fatal(err)
		}
		var described []nodeproto.PipelineDescription
		output := captureStderr(t, func() { described, err = inspectTriggerSource(t.Context(), selected, captured, options) })
		if err != nil {
			output += err.Error()
		}
		return err == nil && len(described) == 1 && !strings.Contains(output, secret) && !strings.Contains(output, base64.StdEncoding.EncodeToString([]byte(secret)))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 16}); err != nil {
		t.Fatal(err)
	}
}
