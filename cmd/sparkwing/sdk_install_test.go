package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

func sdkInstallFixture(t *testing.T, sdk engine.SDKKind) (sdkInstaller, string) {
	t.Helper()
	checkout := t.TempDir()
	request, err := json.Marshal(engine.SDKInstallRequest{Directory: checkout, PipelineDir: ".sparkwing", SDK: sdk})
	if err != nil {
		t.Fatal(err)
	}
	installer := sdkInstaller{
		version: "v1.2.3", environment: []string{"PATH=/tools", "CONTROLLER_TOKEN=private", "SPARKWING_RUN_ID=parent"},
		readPin: func(dir string, selected engine.SDKKind) (engine.Pin, error) {
			if dir != checkout || selected != sdk {
				t.Fatalf("pin source: %s %s", dir, selected)
			}
			return engine.Pin{SDK: sdk, Version: "1.2.3", Manifest: ".sparkwing/package.json"}, nil
		},
		versionFor: func(engine.Pin) (string, error) { return "v1.2.3", nil },
		prepare: func(_ context.Context, dir string, selected engine.SDKKind, env []string, diagnostics io.Writer) (engine.SDKCommand, error) {
			if dir != filepath.Join(checkout, ".sparkwing") || selected != sdk || !reflect.DeepEqual(env, []string{"PATH=/tools"}) {
				t.Fatalf("install inputs: %s %s %v", dir, selected, env)
			}
			if _, err := io.WriteString(diagnostics, "installed\n"); err != nil {
				return engine.SDKCommand{}, err
			}
			return engine.SDKCommand{Directory: dir, Command: []string{filepath.Join(checkout, "pipeline")}}, nil
		},
	}
	return installer, string(request)
}

func TestSDKInstallUsesPinAndFilteredEnvironment(t *testing.T) {
	for _, sdk := range []engine.SDKKind{engine.GoSDK, engine.TypeScriptSDK} {
		t.Run(string(sdk), func(t *testing.T) {
			installer, request := sdkInstallFixture(t, sdk)
			var out, diagnostics bytes.Buffer
			if err := installer.run(t.Context(), nil, strings.NewReader(request), &out, &diagnostics); err != nil {
				t.Fatal(err)
			}
			var command engine.SDKCommand
			if err := json.Unmarshal(out.Bytes(), &command); err != nil {
				t.Fatal(err)
			}
			if len(command.Command) != 1 || !filepath.IsAbs(command.Command[0]) || diagnostics.String() != "installed\n" {
				t.Fatalf("result: %+v diagnostics=%s", command, &diagnostics)
			}
		})
	}
}

func TestSDKInstallHiddenDispatch(t *testing.T) {
	output := captureStdout(t, func() {
		if err := dispatchCommand([]string{"__sdk-install", "--help", "--output", "json"}); err != nil {
			t.Fatal(err)
		}
	})
	if !json.Valid([]byte(output)) || !strings.Contains(output, "__sdk-install") {
		t.Fatalf("hidden installer help did not dispatch: %s", output)
	}
}

func TestSDKInstallRejectsInputBeforeDependencies(t *testing.T) {
	for _, body := range []string{
		"", "null", `{}`, `{"directory":"relative","pipeline_dir":".sparkwing","sdk":"go"}`,
		`{"directory":"/checkout","pipeline_dir":"../escape","sdk":"go"}`,
		`{"directory":"/checkout","pipeline_dir":"/escape","sdk":"go"}`,
		`{"directory":"/checkout","pipeline_dir":".sparkwing","sdk":"other"}`,
		`{"directory":"/checkout","pipeline_dir":".sparkwing","sdk":"go","environment":["TOKEN=private"]}`,
		`{"directory":"/checkout","pipeline_dir":".sparkwing","sdk":"go"} {}`,
	} {
		var out bytes.Buffer
		if err := (sdkInstaller{}).run(t.Context(), nil, strings.NewReader(body), &out, io.Discard); err == nil || out.Len() != 0 {
			t.Fatalf("accepted %q: %v %s", body, err, &out)
		}
	}
	for _, args := range [][]string{{"--unknown"}, {"extra"}, {"--output", "yaml"}} {
		if err := (sdkInstaller{}).run(t.Context(), args, nil, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	var help bytes.Buffer
	if err := (sdkInstaller{}).run(t.Context(), []string{"--help"}, nil, &help, nil); err != nil || help.Len() == 0 {
		t.Fatalf("help: %v", err)
	}
}

func TestSDKInstallRefusesAnotherEngineAndPinDrift(t *testing.T) {
	for _, drift := range []bool{false, true} {
		installer, request := sdkInstallFixture(t, engine.GoSDK)
		if drift {
			read := installer.readPin
			calls := 0
			installer.readPin = func(dir string, sdk engine.SDKKind) (engine.Pin, error) {
				pin, err := read(dir, sdk)
				calls++
				if calls > 1 {
					pin.InputsDigest = "changed"
				}
				return pin, err
			}
		} else {
			installer.version = "v1.2.4"
			installer.prepare = func(context.Context, string, engine.SDKKind, []string, io.Writer) (engine.SDKCommand, error) {
				t.Fatal("wrong engine installed SDK")
				return engine.SDKCommand{}, nil
			}
		}
		var out bytes.Buffer
		if err := installer.run(t.Context(), nil, strings.NewReader(request), &out, io.Discard); err == nil || out.Len() != 0 {
			t.Fatalf("drift=%t: %v %s", drift, err, &out)
		}
	}
}

func TestSDKInstallBindsPreparationToPinManifest(t *testing.T) {
	installer, request := sdkInstallFixture(t, engine.GoSDK)
	read := installer.readPin
	installer.readPin = func(dir string, sdk engine.SDKKind) (engine.Pin, error) {
		pin, err := read(dir, sdk)
		pin.Manifest = "other/go.mod"
		return pin, err
	}
	installer.prepare = func(context.Context, string, engine.SDKKind, []string, io.Writer) (engine.SDKCommand, error) {
		t.Fatal("installed a directory with a different SDK pin")
		return engine.SDKCommand{}, nil
	}
	if err := installer.run(t.Context(), nil, strings.NewReader(request), io.Discard, io.Discard); err == nil {
		t.Fatal("selected pin did not bind the preparation directory")
	}
}

func TestSDKInstallMasksDiagnosticsAndFailure(t *testing.T) {
	installer, request := sdkInstallFixture(t, engine.GoSDK)
	installer.environment = []string{"PATH=/tools", "HTTPS_PROXY=http://alice:p%40ss@proxy.example"}
	failure := errors.New("tool printed p@ss")
	installer.prepare = func(_ context.Context, dir string, sdk engine.SDKKind, env []string, diagnostics io.Writer) (engine.SDKCommand, error) {
		for _, text := range []string{"download as ali", "ce and p@", "ss\n"} {
			if _, err := io.WriteString(diagnostics, text); err != nil {
				return engine.SDKCommand{}, err
			}
		}
		return engine.SDKCommand{}, failure
	}
	var out, diagnostics bytes.Buffer
	err := installer.run(t.Context(), nil, strings.NewReader(request), &out, &diagnostics)
	if !errors.Is(err, failure) || out.Len() != 0 || strings.Contains(err.Error(), "p@ss") || strings.Contains(diagnostics.String(), "alice") || strings.Contains(diagnostics.String(), "p@ss") {
		t.Fatalf("masking failed: %v %s %s", err, &out, &diagnostics)
	}
}

func TestSDKInstallPreservesCancellationAndWriterErrors(t *testing.T) {
	installer, request := sdkInstallFixture(t, engine.GoSDK)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := installer.run(ctx, nil, nil, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := installer.run(t.Context(), nil, strings.NewReader(request), doctorFailedWriter{}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("result writer: %v", err)
	}
	if err := installer.run(t.Context(), nil, strings.NewReader(request), io.Discard, doctorFailedWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("diagnostics writer: %v", err)
	}
	installer.prepare = func(_ context.Context, dir string, _ engine.SDKKind, _ []string, _ io.Writer) (engine.SDKCommand, error) {
		return engine.SDKCommand{Directory: dir, Command: []string{"relative"}}, nil
	}
	if err := installer.run(t.Context(), nil, strings.NewReader(request), io.Discard, io.Discard); err == nil {
		t.Fatal("accepted relative executable")
	}
}

func TestSDKInstallMasksTypedErrorsThroughCLIFormatting(t *testing.T) {
	installer, request := sdkInstallFixture(t, engine.GoSDK)
	installer.environment = []string{"PATH=/tools", "HTTPS_PROXY=http://alice:p%40ss@proxy.test"}
	failure := &apierr.Error{Code: apierr.BadRequest, Message: "failed for alice", Hint: "check p@ss", Details: map[string]any{"alice": []any{"p@ss", 9007199254740993}}}
	installer.prepare = func(context.Context, string, engine.SDKKind, []string, io.Writer) (engine.SDKCommand, error) {
		return engine.SDKCommand{}, failure
	}
	err := installer.run(t.Context(), nil, strings.NewReader(request), io.Discard, io.Discard)
	if !errors.Is(err, failure) {
		t.Fatalf("typed error lost its cause: %v", err)
	}
	for _, format := range []string{"json", "plain"} {
		inv, parseErr := clisurface.Parse("sparkwing", []string{"__sdk-install", "-o", format})
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		var out bytes.Buffer
		encode := func(w io.Writer, failure *apierr.Error) error {
			return json.NewEncoder(w).Encode(apierr.Envelope{Error: failure})
		}
		if writeErr := writeInvocationError(&out, &invocationError{inv: inv, cause: err}, encode); writeErr != nil {
			t.Fatal(writeErr)
		}
		if strings.Contains(out.String(), "alice") || strings.Contains(out.String(), "p@ss") || !strings.Contains(out.String(), "***") {
			t.Fatalf("%s formatting exposed typed error fields: %s", format, &out)
		}
		if format == "json" && (!strings.Contains(out.String(), `"code":"bad_request"`) || !strings.Contains(out.String(), "9007199254740993")) {
			t.Fatalf("typed error semantics changed: %s", &out)
		}
	}
	if failure.Message != "failed for alice" || failure.Hint != "check p@ss" {
		t.Fatal("formatting mutated the shared error")
	}
}
