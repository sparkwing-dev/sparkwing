package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/ndjson"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

type fakeUpgradeResolver struct {
	request  engine.UpgradeRequest
	verifier engine.UpgradeVerifier
	result   engine.UpgradeResult
	err      error
}

func (f *fakeUpgradeResolver) Upgrade(_ context.Context, request engine.UpgradeRequest, verifier engine.UpgradeVerifier) (engine.UpgradeResult, error) {
	f.request, f.verifier = request, verifier
	return f.result, f.err
}

func upgradeFixture(t *testing.T, sdk engine.SDKKind) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".sparkwing")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name, body := "main.go", "package main\n"
	if sdk == engine.TypeScriptSDK {
		name, body = "package.json", `{"dependencies":{"@sparkwing/sdk":"1.0.0"}}`
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestUpgradeSelectsRepositorySDKAndReportsPreview(t *testing.T) {
	for _, sdk := range []engine.SDKKind{engine.GoSDK, engine.TypeScriptSDK} {
		t.Run(string(sdk), func(t *testing.T) {
			root := upgradeFixture(t, sdk)
			fake := &fakeUpgradeResolver{result: engine.UpgradeResult{DryRun: true, Plan: engine.UpgradePlan{
				Before: engine.Pin{SDK: sdk, Version: "1.0.0"}, After: engine.Pin{SDK: sdk, Version: "1.1.0"},
				Edits:   []engine.PinEdit{{Path: ".sparkwing/manifest", BeforeDigest: "digest", After: []byte("proposed")}},
				Notices: []engine.Notice{{Kind: engine.NoticeIndexFallback, Reason: "using verified cached policy", Hint: "restore connectivity"}},
			}}}
			var out bytes.Buffer
			command := upgradeCommand{newResolver: func() (upgradeResolver, error) { return fake, nil }, out: &out}
			if err := command.runArgs(t.Context(), []string{"--checkout", root, "--version=v1.1.0", "--dry-run", "--output=json"}); err != nil {
				t.Fatal(err)
			}
			if fake.request != (engine.UpgradeRequest{Checkout: root, SDK: sdk, Version: "v1.1.0", DryRun: true}) {
				t.Fatalf("request: %#v", fake.request)
			}
			var got upgradeReport
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Applied || got.Verified || !got.DryRun || got.Before.Version != "1.0.0" || got.After.Version != "1.1.0" || len(got.Files) != 1 || len(got.Notices) != 1 {
				t.Fatalf("preview: %s", &out)
			}
		})
	}
}

func TestUpgradeRejectsGrammarBeforeOpeningResolver(t *testing.T) {
	for _, args := range [][]string{{"--output=xml"}, {"--version"}, {"extra"}, {"--unknown"}} {
		command := upgradeCommand{newResolver: func() (upgradeResolver, error) { t.Fatal("opened resolver on invalid invocation"); return nil, nil }}
		if err := command.runArgs(t.Context(), args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestUpgradeHelpDoesNotOpenRepositoryOrResolver(t *testing.T) {
	var out bytes.Buffer
	command := upgradeCommand{out: &out}
	if err := command.runArgs(t.Context(), []string{"--checkout=/does-not-exist", "--help", "--output=plain"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "--dry-run") || !strings.Contains(out.String(), "--version") {
		t.Fatalf("help: %s", &out)
	}
}

type upgradeFailedWriter struct{}

func (upgradeFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestUpgradeReportPreservesOutputFailure(t *testing.T) {
	for _, format := range []string{"json", "plain", "pretty"} {
		if err := writeUpgradeReport(upgradeFailedWriter{}, format, engine.UpgradeResult{}); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("%s output failure = %v", format, err)
		}
	}
}

func TestUpgradePreservesPolicyAndVerificationFailures(t *testing.T) {
	root := upgradeFixture(t, engine.GoSDK)
	for _, failure := range []error{&apierr.Error{Code: apierr.VersionRevoked, Message: "signed security reason", Hint: "sparkwing upgrade"}, errors.New("type checking failed")} {
		fake := &fakeUpgradeResolver{err: failure}
		var out bytes.Buffer
		command := upgradeCommand{newResolver: func() (upgradeResolver, error) { return fake, nil }, out: &out}
		if err := command.runArgs(t.Context(), []string{"--checkout", root}); !errors.Is(err, failure) {
			t.Fatalf("error = %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("failed upgrade reported success: %s", &out)
		}
	}
}

func TestUpgradeVerifiedResult(t *testing.T) {
	root := upgradeFixture(t, engine.GoSDK)
	verifier := &upgradeVerifier{}
	fake := &fakeUpgradeResolver{result: engine.UpgradeResult{Applied: true, Verified: true, Plan: engine.UpgradePlan{Before: engine.Pin{Version: "v1.0.0"}, After: engine.Pin{Version: "v1.1.0"}}}}
	var out bytes.Buffer
	command := upgradeCommand{newResolver: func() (upgradeResolver, error) { return fake, nil }, verifier: verifier, out: &out}
	if err := command.runArgs(t.Context(), []string{"--checkout", root, "--output=pretty"}); err != nil {
		t.Fatal(err)
	}
	if fake.verifier != verifier || !strings.Contains(out.String(), "Verified and applied") {
		t.Fatalf("result: %s", &out)
	}
	fake.result.Verified = false
	if err := command.runArgs(t.Context(), []string{"--checkout", root}); err == nil {
		t.Fatal("unverified result reported as success")
	}
}

func TestUpgradePlainReportsTargetVersion(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		for _, version := range []string{"v1.2.3", "1.2.3"} {
			result := engine.UpgradeResult{DryRun: dryRun, Applied: !dryRun, Verified: !dryRun, Plan: engine.UpgradePlan{
				Before: engine.Pin{Version: "1.0.0"}, After: engine.Pin{Version: version},
				Edits:   []engine.PinEdit{{Path: "package-lock.json"}},
				Notices: []engine.Notice{{Reason: "cached policy", Hint: "restore connectivity"}},
			}}
			var out bytes.Buffer
			if err := writeUpgradeReport(&out, "plain", result); err != nil {
				t.Fatal(err)
			}
			if out.String() != version+"\n" {
				t.Fatalf("plain result: %q, want version %q", out.String(), version)
			}
		}
	}
}

func TestUpgradeVerifierUsesStagedCheckoutAndExactEngine(t *testing.T) {
	for _, sdk := range []engine.SDKKind{engine.GoSDK, engine.TypeScriptSDK} {
		t.Run(string(sdk), func(t *testing.T) {
			root := upgradeFixture(t, sdk)
			selected := engine.Resolved{Executable: filepath.Join(root, "verified-engine"), Identity: engine.Identity{Pin: engine.Pin{SDK: sdk}}}
			var catalog bytes.Buffer
			if err := ndjson.Write(&catalog, []PipelineIndex{{Name: "first", Short: "Build and test", Entrypoint: ".sparkwing/main.go", Triggers: []string{"push"}}, {Name: "hidden"}}); err != nil {
				t.Fatal(err)
			}
			var commands [][]string
			installs := 0
			var kinds []procenv.Kind
			verifier := upgradeVerifier{environment: []string{"PATH=/tools"}, install: func(ctx context.Context, got engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
				installs++
				if ctx.Err() != nil || !reflect.DeepEqual(got, selected) || request.Directory != root || request.PipelineDir != ".sparkwing" || request.SDK != sdk || !reflect.DeepEqual(request.Environment, []string{"CLEAN=yes"}) {
					t.Fatalf("selected installer: %+v %+v", got, request)
				}
				return engine.SDKCommand{Directory: filepath.Join(root, ".sparkwing"), Command: []string{filepath.Join(root, "sdk-entry")}}, nil
			}, buildEnv: func(kind procenv.Kind, _ procenv.Base, _ procenv.Runtime) (procenv.Env, error) {
				kinds = append(kinds, kind)
				return procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: []string{"CLEAN=yes"}}, procenv.Runtime{})
			}, exec: func(_ context.Context, dir string, env procenv.Env, program string, args ...string) ([]byte, error) {
				if dir != root && dir != filepath.Join(root, ".sparkwing") {
					t.Fatalf("ran outside staged checkout: %s", dir)
				}
				if env.Lookup("CLEAN") != "yes" {
					t.Fatalf("unfiltered environment: %v", env)
				}
				commands = append(commands, append([]string{program}, args...))
				if program == selected.Executable && len(args) > 1 && args[1] == "list" {
					return catalog.Bytes(), nil
				}
				return nil, nil
			}}
			if err := verifier.Verify(t.Context(), root, selected); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(kinds, []procenv.Kind{procenv.Install, procenv.HostChild}) {
				t.Fatalf("environment boundaries: %v", kinds)
			}
			if installs != 1 || len(commands) != 6 {
				t.Fatalf("commands: %v", commands)
			}
			wantNative := [][]string{{"go", "build", "-mod=readonly", "-o", os.DevNull, "./..."}}
			if sdk == engine.TypeScriptSDK {
				wantNative = [][]string{{"npm", "exec", "--offline", "--no", "--", "tsc", "--noEmit"}}
			}
			if !reflect.DeepEqual(commands[:1], wantNative) {
				t.Fatalf("dependency/type checks: %v", commands[:1])
			}
			for _, command := range commands[1:] {
				if command[0] != selected.Executable || command[1] != "pipeline" || (command[2] != "list" && command[2] != "describe" && command[2] != "plan") {
					t.Fatalf("unverified engine or body command: %v", command)
				}
			}
			if !reflect.DeepEqual(commands[1][1:], []string{"pipeline", "list", "--all", "--output=json"}) {
				t.Fatalf("hidden pipelines excluded: %v", commands[1])
			}
		})
	}
}

func TestUpgradeVerifierRejectsMalformedPipelineList(t *testing.T) {
	for _, body := range []string{"", "[]", "null", `{"name":""}`, "{\"name\":\"check\"}\n{\"name\":\"check\"}\n", `{"name":"check"} trailing`} {
		if _, err := upgradePipelineNames([]byte(body)); err == nil {
			t.Fatalf("accepted malformed list %q", body)
		}
	}
}

func TestUpgradeVerifierStopsAtFailure(t *testing.T) {
	root := upgradeFixture(t, engine.GoSDK)
	for _, failAt := range []int{1, 2, 3, 4, 5} {
		calls := 0
		failure := errors.New("verification failed")
		verifier := upgradeVerifier{install: func(context.Context, engine.Resolved, engine.SDKInstallRequest) (engine.SDKCommand, error) {
			calls++
			if calls == failAt {
				return engine.SDKCommand{}, failure
			}
			return engine.SDKCommand{Directory: filepath.Join(root, ".sparkwing"), Command: []string{filepath.Join(root, "sdk-entry")}}, nil
		}, buildEnv: func(procenv.Kind, procenv.Base, procenv.Runtime) (procenv.Env, error) { return procenv.Env{}, nil }, exec: func(context.Context, string, procenv.Env, string, ...string) ([]byte, error) {
			calls++
			if calls == failAt {
				return nil, failure
			}
			return []byte("{\"name\":\"check\"}\n"), nil
		}}
		err := verifier.Verify(t.Context(), root, engine.Resolved{Executable: filepath.Join(root, "engine"), Identity: engine.Identity{Pin: engine.Pin{SDK: engine.GoSDK}}})
		if !errors.Is(err, failure) || calls != failAt {
			t.Fatalf("failure %d: calls=%d error=%v", failAt, calls, err)
		}
	}
}

func TestUpgradeVerifierRefusesInvalidInputsBeforeInstalling(t *testing.T) {
	root := upgradeFixture(t, engine.GoSDK)
	for _, selected := range []engine.Resolved{
		{Executable: "relative", Identity: engine.Identity{Pin: engine.Pin{SDK: engine.GoSDK}}},
		{Executable: filepath.Join(root, "engine"), Identity: engine.Identity{Pin: engine.Pin{SDK: "other"}}},
	} {
		verifier := upgradeVerifier{
			install: func(context.Context, engine.Resolved, engine.SDKInstallRequest) (engine.SDKCommand, error) {
				t.Fatal("installed invalid input")
				return engine.SDKCommand{}, nil
			},
			buildEnv: procenv.Build,
		}
		if err := verifier.Verify(t.Context(), root, selected); err == nil {
			t.Fatalf("accepted %+v", selected)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	verifier := upgradeVerifier{}
	if err := verifier.Verify(ctx, root, engine.Resolved{Executable: filepath.Join(root, "engine")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestUpgradeVerifierRequiresSelectedInstallerResult(t *testing.T) {
	root := upgradeFixture(t, engine.GoSDK)
	for _, command := range []engine.SDKCommand{
		{},
		{Directory: root, Command: []string{filepath.Join(root, "entry")}},
		{Directory: filepath.Join(root, ".sparkwing"), Command: []string{"relative"}},
	} {
		verifier := upgradeVerifier{
			buildEnv: procenv.Build,
			install: func(context.Context, engine.Resolved, engine.SDKInstallRequest) (engine.SDKCommand, error) {
				return command, nil
			},
			exec: func(context.Context, string, procenv.Env, string, ...string) ([]byte, error) {
				t.Fatal("checked after invalid installation")
				return nil, nil
			},
		}
		if err := verifier.Verify(t.Context(), root, engine.Resolved{Executable: filepath.Join(root, "engine"), Identity: engine.Identity{Pin: engine.Pin{SDK: engine.GoSDK}}}); err == nil {
			t.Fatalf("accepted %+v", command)
		}
	}
}

func FuzzUpgradeVerifierMasksStageFailures(f *testing.F) {
	f.Add("private-upgrade-credential")
	f.Add("secret with spaces and : / ?")
	root := f.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".sparkwing"), 0o700); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sparkwing", "main.go"), []byte("package main\n"), 0o600); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, payload string) {
		secret := "private-" + strings.ReplaceAll(strings.ToValidUTF8(payload, "?"), "\x00", "?") + "-credential"
		encoded := base64.StdEncoding.EncodeToString([]byte(secret))
		for failAt := 0; failAt < 5; failAt++ {
			calls := 0
			failure := &apierr.Error{Code: apierr.Internal, Message: secret, Hint: encoded, Details: map[string]any{secret: []string{encoded, secret}}}
			fail := func() error {
				calls++
				if calls == failAt+1 {
					return failure
				}
				return nil
			}
			verifier := upgradeVerifier{
				environment: []string{"SPARKWING_TOKEN=" + secret, "HTTPS_PROXY=http://" + url.UserPassword("upgrade", secret).String() + "@proxy.example"}, buildEnv: procenv.Build,
				install: func(_ context.Context, _ engine.Resolved, request engine.SDKInstallRequest) (engine.SDKCommand, error) {
					if strings.Contains(strings.Join(request.Environment, "\n"), "SPARKWING_TOKEN=") {
						t.Fatal("installer received a credential")
					}
					return engine.SDKCommand{Directory: filepath.Join(root, ".sparkwing"), Command: []string{filepath.Join(root, "entry")}}, fail()
				},
				exec: func(_ context.Context, _ string, env procenv.Env, _ string, _ ...string) ([]byte, error) {
					if strings.Contains(strings.Join(slices.Collect(env.Values()), "\n"), "SPARKWING_TOKEN=") {
						t.Fatal("verification received a credential")
					}
					return []byte("{\"name\":\"check\"}\n"), fail()
				},
			}
			err := verifier.Verify(t.Context(), root, engine.Resolved{Executable: filepath.Join(root, "engine"), Identity: engine.Identity{Pin: engine.Pin{SDK: engine.GoSDK}}})
			if !errors.Is(err, failure) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), encoded) {
				t.Fatalf("stage %d: credential leaked or cause lost", failAt)
			}
			for _, format := range []string{"json", "pretty", "plain"} {
				inv, parseErr := clisurface.Parse("sparkwing", []string{"upgrade", "-o", format})
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				var out bytes.Buffer
				encode := func(w io.Writer, failure *apierr.Error) error {
					return json.NewEncoder(w).Encode(apierr.Envelope{Error: failure})
				}
				if err := writeInvocationError(&out, &invocationError{inv: inv, cause: err}, encode); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(out.String(), secret) || strings.Contains(out.String(), encoded) {
					t.Fatalf("stage %d %s: credential leaked", failAt, format)
				}
			}
		}
	})
}
