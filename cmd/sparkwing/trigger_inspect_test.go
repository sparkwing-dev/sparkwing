package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func triggerInspectionFixture(t *testing.T) (triggerSourceInput, triggerInspection, *source.Entry) {
	t.Helper()
	repo, sha := triggerPreparationRepo(t)
	runSnapshotGit(t, repo, "update-ref", "refs/remotes/origin/main", sha)
	entry := &source.Entry{
		Directory: filepath.Join(t.TempDir(), "acquired"), RepoKey: "gh:17", SHA: sha,
		Command: []string{filepath.Join(repo, "sdk")},
		Engine: engine.Resolved{
			Identity:   engine.Identity{Pin: engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}, Version: "v1.2.3", ManifestDigest: "verified-manifest"},
			Executable: filepath.Join(repo, "selected-engine"), AssetDigest: "verified-asset", Target: releaseasset.Target{GOOS: "linux", GOARCH: "amd64"},
		},
	}
	deps := triggerInspection{
		options: source.Options{SDK: engine.GoSDK, Target: entry.Engine.Target},
		acquire: func(context.Context, source.Spec, source.Options) (*source.Entry, error) { return entry, nil },
		close:   func(*source.Entry) error { return nil },
		buildEnv: func(k procenv.Kind, base procenv.Base, _ procenv.Runtime) ([]string, error) {
			if k != procenv.HostChild || !base.Local || base.Masker == nil {
				t.Fatal("inspection bypassed the captured host environment")
			}
			return []string{"USER_SETTING=captured"}, nil
		},
		exec: func(context.Context, string, []string, string, ...string) ([]byte, error) {
			return []byte(`{"name":"deploy-prod","entrypoint":"Deploy","args":[{"name":"region","type":"string","required":true},{"name":"password","type":"string","secret":true}]}`), nil
		},
	}
	return triggerPreparationInput(repo), deps, entry
}

func TestInspectTriggerUsesAcquiredSourceAndSelectedEngine(t *testing.T) {
	input, deps, entry := triggerInspectionFixture(t)
	selection, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	selection.source.Ref = "separate-pipeline-commit"
	calls := []string{}
	deps.acquire = func(_ context.Context, spec source.Spec, options source.Options) (*source.Entry, error) {
		calls = append(calls, "acquire")
		if spec.RepoKey != "gh:17" || spec.SHA != selection.source.Ref || spec.RemoteURL != selection.source.Location.RemoteURL || spec.LocalCheckout != "" || spec.PipelineDir != ".sparkwing" || spec.Engine != nil || spec.BundleDigest != "" {
			t.Fatalf("acquisition identity changed: %+v", spec)
		}
		if !options.Base.Local || !reflect.DeepEqual(options.Base.Captured, input.environment) || options.Base.Masker == nil || options.SDK != engine.GoSDK || options.Target != entry.Engine.Target {
			t.Fatal("acquisition changed process or engine policy")
		}
		entry.SHA = spec.SHA
		return entry, nil
	}
	deps.exec = func(_ context.Context, dir string, env []string, program string, args ...string) ([]byte, error) {
		calls = append(calls, "describe")
		if dir != entry.Directory || program != entry.Engine.Executable || !reflect.DeepEqual(env, []string{"USER_SETTING=captured"}) || !reflect.DeepEqual(args, []string{"pipeline", "describe", "--pipeline=deploy-prod", "--output=json"}) {
			t.Fatalf("inspection substituted an engine or binding: %s %s %v", dir, program, args)
		}
		return []byte(`{"name":"deploy-prod","entrypoint":"Deploy","args":[{"name":"region","type":"string","required":true}]}`), nil
	}
	deps.close = func(got *source.Entry) error {
		calls = append(calls, "close")
		if got != entry {
			t.Fatal("released another acquisition")
		}
		return nil
	}
	described, err := inspectTriggerSource(t.Context(), selection, input.environment, deps)
	if err != nil || len(described) != 1 || described[0].Name != "deploy-prod" || described[0].Args[0].Name != "region" || !reflect.DeepEqual(calls, []string{"acquire", "describe", "close"}) {
		t.Fatalf("inspection: %+v calls=%v err=%v", described, calls, err)
	}
}

func TestInspectTriggerClosesAcquisitionOnEveryFailure(t *testing.T) {
	for _, mode := range []string{"acquire", "nil entry", "wrong commit", "wrong repo", "engine", "target", "relative engine", "relative directory", "notice", "environment", "describe", "wrong alias", "wrong entrypoint", "trailing", "null", "close", "cancelled before", "cancelled acquisition", "cancelled describe", "cancelled close"} {
		t.Run(mode, func(t *testing.T) {
			input, deps, entry := triggerInspectionFixture(t)
			selection, err := prepareTriggerSource(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("inspection failed")
			closed, described := 0, 0
			deps.close = func(*source.Entry) error {
				closed++
				if mode == "cancelled close" {
					cancel()
				}
				if mode == "close" || mode == "describe" {
					return failure
				}
				return nil
			}
			execute := deps.exec
			deps.exec = func(ctx context.Context, dir string, env []string, program string, args ...string) ([]byte, error) {
				described++
				switch mode {
				case "describe":
					return nil, failure
				case "wrong alias":
					return []byte(`{"name":"another","entrypoint":"Deploy"}`), nil
				case "wrong entrypoint":
					return []byte(`{"name":"deploy-prod","entrypoint":"Another"}`), nil
				case "trailing":
					return []byte(`{"name":"deploy-prod","entrypoint":"Deploy"}{}`), nil
				case "null":
					return []byte(`null`), nil
				case "cancelled describe":
					cancel()
				}
				return execute(ctx, dir, env, program, args...)
			}
			switch mode {
			case "acquire":
				deps.acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) { return entry, failure }
			case "nil entry":
				deps.acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) { return nil, nil }
			case "wrong commit":
				entry.SHA = "another"
			case "wrong repo":
				entry.RepoKey = "gh:18"
			case "engine":
				entry.Engine.Identity.Version = ""
			case "target":
				entry.Engine.Target.GOARCH = "arm64"
			case "relative engine":
				entry.Engine.Executable = "ambient-engine"
			case "relative directory":
				entry.Directory = "ambient-checkout"
			case "notice":
				entry.Engine.Notices = []engine.Notice{{Kind: engine.NoticeEnforcement, Reason: "version enforcement is scheduled"}}
				deps.diagnostics = upgradeFailedWriter{}
			case "environment":
				deps.buildEnv = func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error) { return nil, failure }
			case "cancelled acquisition":
				deps.acquire = func(context.Context, source.Spec, source.Options) (*source.Entry, error) { cancel(); return entry, nil }
			case "cancelled before":
				cancel()
			}
			_, err = inspectTriggerSource(ctx, selection, input.environment, deps)
			wantClosed := 1
			if mode == "nil entry" || mode == "cancelled before" {
				wantClosed = 0
			}
			if err == nil || closed != wantClosed {
				t.Fatalf("inspection failure: closed=%d described=%d err=%v", closed, described, err)
			}
			if strings.HasPrefix(mode, "cancelled") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			if mode == "notice" && (!errors.Is(err, io.ErrClosedPipe) || described != 0) {
				t.Fatalf("notice output failure was ignored: %v", err)
			}
			if mode == "acquire" || mode == "environment" || mode == "describe" || mode == "close" {
				if !errors.Is(err, failure) {
					t.Fatalf("dependency failure lost: %v", err)
				}
			}
		})
	}
}

func TestPrepareInspectedTriggerSubmitsTheSameBinding(t *testing.T) {
	input, deps, _ := triggerInspectionFixture(t)
	selection, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err := selection.checkReachable(t.Context()); err != nil {
		t.Fatal(err)
	}
	described, err := inspectTriggerSource(t.Context(), selection, input.environment, deps)
	if err != nil {
		t.Fatal(err)
	}
	request, err := prepareTriggerRequest(triggerRequestInput{source: selection, described: described, args: []string{"--region=west", "--password=private-password"}, settings: settings.Inputs{Context: settings.Context{NumCPU: 4}}, idempotencyKey: "inspected"})
	if err != nil {
		t.Fatal(err)
	}
	err = submitTriggeredRun(t.Context(), request, true, "json", &strings.Builder{}, triggerSubmission{Controller: initialTriggerController{create: func(_ context.Context, got controllerwire.CreateRunRequest) (controllerwire.CreateRunResponse, error) {
		if got.Request.Source != request.Request.Source || !reflect.DeepEqual(got.Request.Git, request.Request.Git) || !got.Request.SecretArgs.Set || !reflect.DeepEqual(got.Request.SecretArgs.Value, []string{"password"}) || got.IdempotencyKey != "inspected" {
			t.Fatal("inspected source or classification changed before intake")
		}
		got.Request.Team = "server-team"
		return initialTriggerResponse(t, got), nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
}

func FuzzTriggerInspectionCannotSubstituteSourceIdentity(f *testing.F) {
	f.Add("other-commit", "gh:18", "Another")
	f.Add("", "", "")
	f.Add("exact", "gh:17", "Deploy")
	f.Add("exact", "gh:17", "Another")
	f.Fuzz(func(t *testing.T, commit, repo, entrypoint string) {
		selection := preparedTriggerSource{pipeline: "deploy-prod"}
		selection.git.Repository.Key = "gh:17"
		selection.source.Ref, selection.source.Entrypoint = "exact", "Deploy"
		acquired := &source.Entry{Directory: "/acquired", SHA: commit, RepoKey: repo, Engine: engine.Resolved{Executable: "/verified", AssetDigest: "asset", Identity: engine.Identity{Version: "v1.2.3", ManifestDigest: "manifest", Pin: engine.Pin{SDK: engine.GoSDK}}}}
		closed, executed := 0, false
		deps := triggerInspection{
			options:  source.Options{SDK: engine.GoSDK},
			acquire:  func(context.Context, source.Spec, source.Options) (*source.Entry, error) { return acquired, nil },
			close:    func(*source.Entry) error { closed++; return nil },
			buildEnv: func(procenv.Kind, procenv.Base, procenv.Runtime) ([]string, error) { return []string{}, nil },
			exec: func(context.Context, string, []string, string, ...string) ([]byte, error) {
				executed = true
				return json.Marshal(map[string]any{"name": "deploy-prod", "entrypoint": entrypoint})
			},
		}
		_, err := inspectTriggerSource(t.Context(), selection, nil, deps)
		validSource := commit == "exact" && repo == "gh:17"
		if closed != 1 || executed != validSource || (err == nil) != (validSource && entrypoint == "Deploy") {
			t.Fatalf("identity substitution: executed=%v closed=%d err=%v", executed, closed, err)
		}
	})
}

func FuzzTriggerInspectionMasksProxyDiagnostics(f *testing.F) {
	f.Add("credential")
	f.Add("嵌入\"'/authority\n")
	f.Fuzz(func(t *testing.T, payload string) {
		secret := "private-" + strings.ReplaceAll(strings.ToValidUTF8(payload, "?"), "\x00", "?") + "-credential"
		encoded := base64.StdEncoding.EncodeToString([]byte(secret))
		selection := preparedTriggerSource{pipeline: "deploy-prod"}
		selection.git.Repository.Key = "gh:17"
		selection.source.Ref, selection.source.Entrypoint = "exact", "Deploy"
		entry := &source.Entry{Directory: "/acquired", SHA: "exact", RepoKey: "gh:17", Engine: engine.Resolved{Executable: "/verified", AssetDigest: "asset", Identity: engine.Identity{Version: "v1.2.3", ManifestDigest: "manifest", Pin: engine.Pin{SDK: engine.GoSDK}}}}
		entry.Engine.Notices = []engine.Notice{{Kind: engine.NoticeEnforcement, Reason: secret, Hint: encoded}}
		var diagnostics bytes.Buffer
		failure := &apierr.Error{Code: apierr.Internal, Message: secret, Hint: encoded, Details: map[string]any{secret: []string{secret, encoded}}}
		deps := triggerInspection{
			diagnostics: &diagnostics,
			options:     source.Options{SDK: engine.GoSDK},
			acquire:     func(context.Context, source.Spec, source.Options) (*source.Entry, error) { return entry, nil },
			close:       func(*source.Entry) error { return nil },
			exec: func(_ context.Context, _ string, env []string, _ string, _ ...string) ([]byte, error) {
				if strings.Contains(strings.Join(env, "\n"), "SPARKWING_TOKEN=") {
					t.Fatal("credential entered the selected engine environment")
				}
				return nil, failure
			},
		}
		_, err := inspectTriggerSource(t.Context(), selection, []string{"SPARKWING_TOKEN=" + secret, "HTTPS_PROXY=http://" + url.UserPassword("inspection", secret).String() + "@proxy.example"}, deps)
		var canonical *apierr.Error
		if !errors.Is(err, failure) || !errors.As(err, &canonical) {
			t.Fatal("diagnostic lost the dependency failure")
		}
		raw, marshalErr := json.Marshal(canonical)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Contains(err.Error()+string(raw)+diagnostics.String(), secret) || strings.Contains(err.Error()+string(raw)+diagnostics.String(), encoded) {
			t.Fatal("proxy credential entered an inspection diagnostic")
		}
	})
}

func TestInspectionPreservesRequesterSecretMasker(t *testing.T) {
	input, deps, _ := triggerInspectionFixture(t)
	selection, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	masker := secrets.NewMasker()
	masker.Register("private-local-value")
	deps.options.Base.Masker = masker
	deps.exec = func(context.Context, string, []string, string, ...string) ([]byte, error) {
		return nil, errors.New("private-local-value")
	}
	_, err = inspectTriggerSource(t.Context(), selection, input.environment, deps)
	if err == nil || strings.Contains(err.Error(), "private-local-value") {
		t.Fatalf("inspection failure: %v", err)
	}
}
