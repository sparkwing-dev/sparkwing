//go:build unix

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

const burstIntakeSecret = "requester-only-credential-7c5d1f"

func TestBurstLocalFallbackRetainsRequesterSecretsOutsideEnvironmentSnapshot(t *testing.T) {
	burstLocalFallbackSecrets(t, burstIntakeSecret)
}

func FuzzBurstLocalFallbackSecretsNeverEnterEnvironmentSnapshots(f *testing.F) {
	f.Add([]byte("private credential"))
	f.Add([]byte{0, 255, 39, 10})
	f.Fuzz(func(t *testing.T, value []byte) {
		if len(value) > 128 {
			return
		}
		burstLocalFallbackSecrets(t, "requester-only-"+hex.EncodeToString(value))
	})
}

func burstLocalFallbackSecrets(t *testing.T, value string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", home)
	config := filepath.Join(home, "config.yaml")
	t.Setenv("SPARKWING_CONFIG", config)
	t.Setenv("TOKEN", value)
	t.Setenv("BURST_ORDINARY", "retained")
	if err := os.WriteFile(config, []byte("run:\n  routing:\n    route: cloud\n    profile: unreachable\n    local_secrets: [TOKEN]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := burstIntakeRepository(t)
	t.Chdir(repo)
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	captured := os.Environ()
	route, err := loadRunRouting(filepath.Join(repo, ".sparkwing"), "burst")
	if err != nil {
		t.Fatal(err)
	}
	var request controllerwire.CreateRunRequest
	var output bytes.Buffer
	err = runRoutedCommand(t.Context(), "burst", runFlags{}, nil, route, &output, burstRunDependencies{
		connect: func(context.Context, string) (client.EngineOptions, func(), error) {
			return client.EngineOptions{}, nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("unreachable")}
		},
		local: func(ctx context.Context, pipeline string, flags runFlags, args []string) error {
			prepared, err := prepareNativeLocalRun(ctx, home, filepath.Join(repo, ".sparkwing"), pipeline, captured, snapshots, flags)
			if err == nil {
				request, err = prepareLocalRunRequest(ctx, prepared, flags, args)
			}
			return err
		},
	})
	if err != nil || request.LocalSecrets["TOKEN"] != value || len(request.Request.LocalSecretNames) != 1 || !strings.Contains(output.String(), "running locally") {
		t.Fatalf("local fallback lost requester secret classification: names=%v error=%v", request.Request.LocalSecretNames, err)
	}
	environment, err := snapshots.Load(t.Context(), request.Request.Env)
	if err != nil {
		t.Fatal(err)
	}
	ordinary := false
	for _, item := range environment {
		if strings.HasPrefix(item, "TOKEN=") || strings.Contains(item, value) {
			t.Fatal("requester secret entered the persisted local environment")
		}
		ordinary = ordinary || item == "BURST_ORDINARY=retained"
	}
	if !ordinary {
		t.Fatal("filtering requester secrets lost ordinary local environment")
	}
	files, err := os.ReadDir(filepath.Join(home, "environments"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() == "salt.key" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(home, "environments", file.Name()))
		var entries [][]byte
		if err != nil || json.Unmarshal(body, &entries) != nil {
			t.Fatal("cannot inspect persisted environment snapshot")
		}
		if bytes.Contains(body, []byte(value)) {
			t.Fatal("requester secret entered an environment snapshot file")
		}
		for _, entry := range entries {
			if bytes.HasPrefix(entry, []byte("TOKEN=")) || bytes.Contains(entry, []byte(value)) {
				t.Fatal("requester secret entered an encoded environment snapshot")
			}
		}
	}
}

func FuzzBurstHostSourceEnvironmentExcludesRequesterValues(f *testing.F) {
	f.Add("requester-credential", "arbitrary-user-value")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, secret, arbitrary string) {
		if strings.ContainsRune(secret+arbitrary, 0) || len(secret)+len(arbitrary) > 4096 {
			t.Skip()
		}
		t.Setenv("TOKEN", secret)
		t.Setenv("BURST_USER_VALUE", arbitrary)
		t.Setenv("GOCACHE", "/host/build-cache")
		t.Setenv("GOMODCACHE", "/host/modules")
		captured := os.Environ()
		t.Setenv("GOCACHE", "/ambient/build-cache")
		t.Setenv("GOMODCACHE", "/ambient/modules")
		options, err := hostSourceOptions(t.TempDir(), captured, secrets.NewMasker(), false)
		if err != nil {
			t.Fatal(err)
		}
		for _, kind := range []procenv.Kind{procenv.Install, procenv.Git, procenv.Plan, procenv.Attempt, procenv.Eval} {
			environment, err := procenv.Build(kind, options.Base, procenv.Runtime{})
			if err != nil {
				t.Fatal(err)
			}
			path, cache, modules := false, false, false
			for _, item := range environment {
				name, _, _ := strings.Cut(item, "=")
				if name == "TOKEN" || name == "BURST_USER_VALUE" {
					t.Fatal("requester value escaped the remote environment policy")
				}
				path = path || name == "PATH"
				cache = cache || item == "GOCACHE=/host/build-cache"
				modules = modules || item == "GOMODCACHE=/host/modules"
			}
			if !path || kind == procenv.Install && (!cache || !modules) {
				t.Fatal("filtered source environment lost its host tools or caches")
			}
		}
	})
}

func TestBurstNativePreparationBuildsUploadedEditedSDK(t *testing.T) {
	t.Setenv("STRAY_REQUESTER_SHELL_NAME", "private")
	home := t.TempDir()
	t.Setenv("GOMODCACHE", offlineGoEnv(t, "GOMODCACHE"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("TOKEN", burstIntakeSecret)
	repo := burstIntakeRepository(t)
	t.Chdir(repo)
	state, server, bearer := burstIntakeController(t, "http://controller.test")
	handler := server.Handler()
	transport := logRoundTrip(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})
	options := client.EngineOptions{URL: "http://controller.test", Bearer: func(context.Context) (string, error) { return bearer, nil }, HTTP: &http.Client{Transport: transport}}
	route := userconfig.RunRouting{Profile: new("cloud"), LocalSecrets: []string{"TOKEN"}, SourceFiles: []string{"declared.txt"}}
	prepared, err := prepareNativeBurst(t.Context(), "burst", runFlags{}, []string{"--block"}, route, options)
	if err != nil {
		t.Fatalf("native burst preparation: %v", err)
	}
	if prepared.LocalSecrets["TOKEN"] != burstIntakeSecret || len(prepared.Request.LocalSecretNames) != 1 || !prepared.Request.Source.BundleDigest.Set {
		t.Fatal("prepared intake lost its private secret or uploaded source")
	}
	if prepared.Request.RequestedArgs["block"] != "true" {
		t.Fatal("burst preparation lost its described boolean argument")
	}
	member, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	created, err := member.CreateRun(t.Context(), prepared)
	if err != nil || created.HereBearer == "" {
		t.Fatalf("real burst intake: %v", err)
	}
	tenant, err := state.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := tenant.ReadSnapshot(t.Context(), created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Run.Request.Request.Env.Names) != 0 {
		t.Fatalf("burst uploaded shell names: %v", snapshot.Run.Request.Request.Env.Names)
	}
	persisted, err := json.Marshal(snapshot)
	if err != nil || bytes.Contains(persisted, []byte(burstIntakeSecret)) || bytes.Contains(persisted, []byte("STRAY_REQUESTER_SHELL_NAME")) {
		t.Fatal("requester shell data entered the Cloud run record")
	}
	options.Bearer = func(context.Context) (string, error) { return created.HereBearer, nil }
	here, err := client.NewEngineClient(options)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := here.Claim(t.Context(), controllerwire.ClaimRequest{RunnerVersion: installedVersion(), RunID: created.RunID, Kinds: []capability.UnitKind{capability.UnitTrigger}, AvailableCores: 4})
	if err != nil || claimed.Claim == nil {
		t.Fatalf("here owner claim: %+v %v", claimed, err)
	}
	if _, err := hereRunInput(created.RunID, *claimed.Claim); err != nil {
		t.Fatalf("public here award cannot be hosted: purpose=%s delivery=%s binding=%+v error=%v", claimed.Claim.Purpose, claimed.Claim.Delivery, claimed.Claim.Binding, err)
	}
	options.Bearer = func(context.Context) (string, error) { return claimed.Claim.Capability, nil }
	spec, err := source.ForRequest(created.Request)
	if err != nil {
		t.Fatal(err)
	}
	acquire, err := hostSourceOptions(home, os.Environ(), secrets.NewMasker(), false)
	if err != nil {
		t.Fatal(err)
	}
	acquire.Access, err = client.NewSourceAccess(options, created.RunID, spec)
	if err != nil {
		t.Fatal(err)
	}
	acquire.BundleTransport = transport
	entry, err := source.Acquire(t.Context(), spec, acquire)
	if err != nil {
		t.Fatalf("compile SDK from uploaded burst bundle: %v", err)
	}
	t.Cleanup(func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	})
	for name, expected := range map[string]string{"tracked.txt": "edited\n", "declared.txt": "included\n"} {
		body, err := os.ReadFile(filepath.Join(entry.Directory, "..", name))
		if err != nil || string(body) != expected {
			t.Fatalf("uploaded source %s: %q %v", name, body, err)
		}
	}
	if _, err := os.Stat(filepath.Join(entry.Directory, "..", ".env")); !os.IsNotExist(err) {
		t.Fatalf("undeclared secret file reached executor checkout: %v", err)
	}
	description, err := repos.Describe(t.Context(), pipelineentry.Command(entry.Command), entry.Directory)
	if err != nil || len(description.Pipelines) != 1 || description.Pipelines[0].Short != "edited SDK" || len(description.Pipelines[0].Secrets) != 1 {
		t.Fatalf("uploaded edited SDK did not execute: %+v %v", description, err)
	}
}

func burstIntakeRepository(t *testing.T) string {
	t.Helper()
	repo := initSnapshotRepo(t)
	directory := filepath.Join(repo, ".sparkwing")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, directory, strings.ReplaceAll(burstIntakePipeline, "edited SDK", "committed SDK"))
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: burst\n    entrypoint: burst\n    source_files: [declared.txt]\n", 0o600)
	writeSnapshotFile(t, repo, "tracked.txt", "committed\n", 0o600)
	runSnapshotGit(t, repo, "add", ".")
	runSnapshotGit(t, repo, "commit", "--quiet", "-m", "burst source")
	runSnapshotGit(t, repo, "remote", "add", "origin", "https://example.test/acme/burst.git")
	writeSnapshotFile(t, repo, ".sparkwing/main.go", burstIntakePipeline, 0o600)
	writeSnapshotFile(t, repo, "tracked.txt", "edited\n", 0o600)
	writeSnapshotFile(t, repo, "declared.txt", "included\n", 0o600)
	writeSnapshotFile(t, repo, ".env", "TOKEN="+burstIntakeSecret+"\n", 0o600)
	return repo
}

func burstIntakeController(t *testing.T, endpoint string) (*store.Store, *controller.Server, string) {
	t.Helper()
	return burstIntakeDeploymentController(t, endpoint, deploymenttest.Team(t))
}

func burstIntakeDeploymentController(t *testing.T, endpoint string, cfg deployment.Config) (*store.Store, *controller.Server, string) {
	t.Helper()
	state, err := teststore.Open(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := state.Close(); err != nil {
			t.Error(err)
		}
	})
	cipher, err := secrets.NewCipher(make([]byte, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	server := controller.NewDeployment(state, cfg, nil).WithSecretsCipher(cipher).WithDashboard(controller.Dashboard{InsecureCookies: strings.HasPrefix(endpoint, "http://")})
	for _, query := range []string{
		`INSERT INTO accounts(id,email,created_at,updated_at) VALUES ('burst-user','burst@example.test',1,1)`,
		`INSERT INTO memberships(team,account_id,role,created_at) VALUES ('default','burst-user','editor',1)`,
	} {
		if _, err := state.DB().ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	session, _, _, err := state.CreateAccountSession(t.Context(), store.Account{ID: "burst-user"}, "default", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(session))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := state.ProvisionCapability(t.Context(), capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: "default", Authority: &authority, Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "burst-user"}, Grants: []capability.Grant{capability.RunsRead, capability.RunsWrite, capability.RunsControl, capability.LogsRead}, Binding: capability.Binding{Kind: capability.BindingNone}})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: endpoint, SigningKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	server.WithDataObjects(objects)
	versions, err := engine.NewResolver(engine.Config{BuildVersion: "development", CacheDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	server.WithEngineController(&controller.EngineController{Store: state, RunnerPolicy: &controller.SignedRunnerAdmission{Resolver: versions}, VersionPolicy: &controller.SignedEngineVersionPolicy{Resolver: versions}})
	return state, server, issued.Raw
}

const burstIntakePipeline = `package main

import (
    "context"
    "encoding/base64"
    "errors"
    "os"
    sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type pipeline struct{ sw.Base }
type inputs struct { Block bool ` + "`flag:\"block\"`" + ` }
type credentials struct { Token string ` + "`sw:\"TOKEN,required\"`" + ` }
func (pipeline) ShortHelp() string { return "edited SDK" }
func (pipeline) Secrets() any { return &credentials{} }
func (pipeline) Plan(_ context.Context, p *sw.Plan, in inputs, _ sw.RunContext) error {
    verify := sw.Job(p, "verify", func(ctx context.Context) error {
        value := sw.PipelineSecrets[credentials](ctx).Token
        if value == "" { return errors.New("missing requester secret") }
        for name, want := range map[string]string{"tracked.txt":"edited\n", "declared.txt":"included\n"} {
            body, err := os.ReadFile(name)
            if err != nil || string(body) != want { return errors.New("wrong uploaded working tree") }
        }
        if _, err := os.Stat(".env"); !os.IsNotExist(err) { return errors.New("undeclared file uploaded") }
        sw.Info(ctx, "burst-edited-ok secret=%s encoded=%s", value, base64.StdEncoding.EncodeToString([]byte(value)))
        if in.Block {
            sw.Info(ctx, "burst-awaiting-cancellation")
            <-ctx.Done()
            return ctx.Err()
        }
        if err := os.MkdirAll("modules", 0o700); err != nil { return err }
        if err := os.WriteFile("modules/dependency", []byte("cached dependency\n"), 0o600); err != nil { return err }
        return os.WriteFile("burst-result.txt", []byte("artifact from edited source\n"), 0o640)
    }).Artifacts("burst-result.txt").Cache(sw.Dir("modules", sw.KeyFromFile("tracked.txt")))
    sw.Job(p, "consume", func(ctx context.Context) error {
        body, err := os.ReadFile("received/burst-result.txt")
        if err != nil || string(body) != "artifact from edited source\n" { return errors.New("missing burst artifact") }
        cached, err := os.ReadFile("modules/dependency")
        if err != nil || string(cached) != "cached dependency\n" { return errors.New("missing burst dependency cache") }
        sw.Info(ctx, "burst-artifact-restored")
        sw.Info(ctx, "burst-cache-restored")
        return nil
    }).Consumes(verify, sw.Into("received")).Cache(sw.Dir("modules", sw.KeyFromFile("tracked.txt")))
    return nil
}
func main() {
    sw.Register("burst", func() sw.Pipeline[inputs] { return pipeline{} })
    sw.Main()
}
`

func TestBurstFixtureSupportsPublicSessionEnrollment(t *testing.T) {
	state, server, _ := burstIntakeController(t, "http://controller.test")
	session, _, _, err := state.CreateAccountSession(t.Context(), store.Account{ID: "burst-user"}, "default", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	manager, err := client.NewSessionRunnerTokenManager(t.Context(), client.EngineOptions{URL: "http://controller.test", HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}}, session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnrollRunner(t.Context(), client.EnrollRunnerRequest{Name: "desktop", Repos: []string{"example.test/acme/*"}, Personal: true}); err != nil {
		t.Fatal(err)
	}
}

func TestBurstNativePreparationRetainsAdvertisedRunOptions(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TOKEN", burstIntakeSecret)
	t.Chdir(burstIntakeRepository(t))
	_, server, bearer := burstIntakeController(t, "http://controller.test")
	handler := server.Handler()
	connection := client.EngineOptions{URL: "http://controller.test", Bearer: func(context.Context) (string, error) { return bearer, nil }, HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})}}
	route := userconfig.RunRouting{Profile: new("cloud"), LocalSecrets: []string{"TOKEN"}, SourceFiles: []string{"declared.txt"}}
	for _, tc := range []struct {
		mode string
		want int
	}{{"front", 1}, {"back", -1}} {
		t.Run(tc.mode, func(t *testing.T) {
			inv, err := clisurface.Parse("sparkwing", []string{"run", "burst", "--sw-no-bincache", "--sw-hash-all-files", "--sw-priority", tc.mode})
			if err != nil {
				t.Fatal(err)
			}
			flags, args := runFlagsFromInvocation(inv)
			request, err := prepareNativeBurst(t.Context(), "burst", flags, args, route, connection)
			if err != nil {
				t.Fatal(err)
			}
			if !request.Request.Source.NoBincache || !request.Request.Source.HashAllFiles || request.Request.Options.Priority.Value != tc.want || request.Request.Options.Priority.Origin.Layer != runspec.LayerFlag {
				t.Fatalf("run options lost before intake: source=%+v priority=%+v", request.Request.Source, request.Request.Options.Priority)
			}
		})
	}
}

func TestBurstNativePreparationSelectsConfiguredSDKEntrypoint(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	t.Setenv("SPARKWING_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv("TOKEN", burstIntakeSecret)
	repo := burstIntakeRepository(t)
	writeSnapshotFile(t, repo, ".sparkwing/sparkwing.yaml", "pipelines:\n  - name: deploy-prod\n    entrypoint: Deploy\n    source_files: [declared.txt]\n", 0o600)
	fixture := strings.Replace(burstIntakePipeline, `sw.Register("burst",`, `sw.Register("Deploy",`, 1)
	fixture = strings.Replace(fixture, "sw.Main()", `sw.Register("deploy-prod",func() sw.Pipeline[sw.NoInputs]{return unrelated{}});sw.Main()`, 1)
	fixture += `type unrelated struct{sw.Base}
func(unrelated) Plan(context.Context,*sw.Plan,sw.NoInputs,sw.RunContext)error{return nil}
`
	writeSnapshotFile(t, repo, ".sparkwing/main.go", fixture, 0o600)
	t.Chdir(repo)
	_, server, bearer := burstIntakeController(t, "http://controller.test")
	connection := client.EngineOptions{URL: "http://controller.test", Bearer: func(context.Context) (string, error) { return bearer, nil }, HTTP: &http.Client{Transport: logRoundTrip(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w.Result(), nil
	})}}
	request, err := prepareNativeBurst(t.Context(), "deploy-prod", runFlags{}, []string{"--block"}, userconfig.RunRouting{Profile: new("cloud"), LocalSecrets: []string{"TOKEN"}, SourceFiles: []string{"declared.txt"}}, connection)
	if err != nil {
		t.Fatal(err)
	}
	if request.Request.Pipeline != "deploy-prod" || request.Request.Source.Entrypoint != "Deploy" || request.Request.RequestedArgs["block"] != "true" || len(request.LocalSecrets) != 1 {
		t.Fatal("native overflow selected alias metadata")
	}
}
