package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
)

func engineSwitchFixture(t testing.TB, sdk engine.SDKKind) (pipelineEngineSwitcher, engine.Resolved, *bytes.Buffer, *int) {
	t.Helper()
	dir := t.TempDir()
	current, selected := filepath.Join(dir, "ambient"), filepath.Join(dir, "selected")
	for _, path := range []string{current, selected} {
		if err := os.WriteFile(path, []byte(path), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	pin := engine.Pin{SDK: sdk, Version: "v1.2.3", Manifest: ".sparkwing/go.mod", InputsDigest: "pin-inputs"}
	if sdk == engine.TypeScriptSDK {
		pin.Version, pin.Manifest, pin.Lockfile = "1.2.3", ".sparkwing/package.json", ".sparkwing/package-lock.json"
	}
	result := engine.Resolved{Identity: engine.Identity{Pin: pin, Version: "v1.2.3", ManifestDigest: "verified-manifest"}, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"}, AssetDigest: "verified-asset", Executable: selected}
	var output bytes.Buffer
	calls := 0
	command := pipelineEngineSwitcher{
		current: current, target: result.Target, diagnostics: &output,
		readPin:    func(string, engine.SDKKind) (engine.Pin, error) { return pin, nil },
		versionFor: func(engine.Pin) (string, error) { return "v1.2.3", nil },
		resolve: func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error) {
			return result, nil
		},
		execute: func(string, []string, []string) error { calls++; return nil },
	}
	return command, result, &output, &calls
}

func TestPipelineEngineSwitchUsesExactPinAndCapturedInvocation(t *testing.T) {
	for _, sdk := range []engine.SDKKind{engine.GoSDK, engine.TypeScriptSDK} {
		for _, platform := range []string{"linux", "darwin", "windows"} {
			t.Run(string(sdk)+"/"+platform, func(t *testing.T) {
				command, selected, _, calls := engineSwitchFixture(t, sdk)
				command.target.GOOS, selected.Target.GOOS = platform, platform
				args, environment := []string{"run", "build", "--", "--tag=release"}, []string{"PATH=/tools", "SPARKWING_TOOLCHAIN_ACTIVE=v9.9.9"}
				order := []string{}
				command.readPin = func(checkout string, got engine.SDKKind) (engine.Pin, error) {
					order = append(order, "pin")
					if checkout != filepath.Join(filepath.Dir(command.current), ".sparkwing") || got != sdk {
						t.Fatalf("pin source changed: %s %s", checkout, got)
					}
					return selected.Identity.Pin, nil
				}
				command.resolve = func(_ context.Context, pin engine.Pin, target releaseasset.Target, mode toolchain.Mode) (engine.Resolved, error) {
					order = append(order, "resolve")
					if pin != selected.Identity.Pin || target != command.target || mode != toolchain.ModeLocal {
						t.Fatalf("selection changed: %+v %+v %s", pin, target, mode)
					}
					return selected, nil
				}
				sentinel := errors.New("process replaced")
				command.execute = func(bin string, gotArgs, gotEnv []string) error {
					*calls++
					order = append(order, "exec")
					if bin != selected.Executable || !reflect.DeepEqual(gotArgs, args) || !reflect.DeepEqual(gotEnv, environment) {
						t.Fatalf("invocation changed: %s %q %q", bin, gotArgs, gotEnv)
					}
					return sentinel
				}
				if err := command.run(t.Context(), filepath.Join(filepath.Dir(command.current), ".sparkwing"), sdk, "local", args, environment); !errors.Is(err, sentinel) || *calls != 1 || !reflect.DeepEqual(order, []string{"pin", "resolve", "exec"}) {
					t.Fatalf("switch: %v calls=%d order=%q", err, *calls, order)
				}
			})
		}
	}
}

func TestPipelineEnginePinComesFromPipelineDirectoryProperty(t *testing.T) {
	for _, sdk := range []engine.SDKKind{engine.GoSDK, engine.TypeScriptSDK} {
		t.Run(string(sdk), func(t *testing.T) {
			root := t.TempDir()
			pipeline := filepath.Join(root, ".sparkwing")
			manifest := "go.mod"
			body := "module pipeline\nrequire github.com/sparkwing-dev/sparkwing v1.2.3\n"
			if sdk == engine.TypeScriptSDK {
				manifest, body = "package.json", `{"dependencies":{"@sparkwing/sdk":"1.2.3"}}`
				writeRepoFile(t, filepath.Join(pipeline, "package-lock.json"), `{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"1.2.3"}},"node_modules/@sparkwing/sdk":{"version":"1.2.3"}}}`)
			}
			writeRepoFile(t, filepath.Join(pipeline, manifest), body)
			pin, err := engine.ReadPin(pipeline, sdk)
			if err != nil {
				t.Fatal(err)
			}
			property := func(minor, mode uint8) bool {
				parent := filepath.Join(root, manifest)
				switch mode % 3 {
				case 0:
					if err := os.Remove(parent); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				case 1:
					parentBody := fmt.Sprintf("module parent\nrequire github.com/sparkwing-dev/sparkwing v1.%d.0\n", minor)
					if sdk == engine.TypeScriptSDK {
						parentBody = fmt.Sprintf(`{"dependencies":{"@sparkwing/sdk":"1.%d.0"}}`, minor)
						writeRepoFile(t, filepath.Join(root, "package-lock.json"), fmt.Sprintf(`{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"1.%d.0"}},"node_modules/@sparkwing/sdk":{"version":"1.%d.0"}}}`, minor, minor))
					}
					writeRepoFile(t, parent, parentBody)
				case 2:
					writeRepoFile(t, parent, "malformed parent manifest")
				}
				command, selected, _, calls := engineSwitchFixture(t, sdk)
				command.readPin, command.versionFor = engine.ReadPin, engine.VersionForPin
				selected.Identity.Pin = pin
				command.resolve = func(_ context.Context, got engine.Pin, _ releaseasset.Target, _ toolchain.Mode) (engine.Resolved, error) {
					if got != pin {
						return engine.Resolved{}, errors.New("parent checkout selected the engine")
					}
					return selected, nil
				}
				report := gatherSDKPinWith(Info{Project: InfoProject{Found: true, SparkwingDir: pipeline}}, sdk, engine.ReadPin, engine.VersionForPin)
				return command.run(t.Context(), pipeline, sdk, "auto", nil, nil) == nil && *calls == 1 && report.Refusal == "" && report.Pin == pin.Version && report.Runs == "v1.2.3"
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSDKPinInspectionRetainsManifestAndLockfileSizes(t *testing.T) {
	for _, sdk := range []engine.SDKKind{engine.GoSDK, engine.TypeScriptSDK} {
		t.Run(string(sdk), func(t *testing.T) {
			dir := t.TempDir()
			if sdk == engine.GoSDK {
				body := "module pipeline\nrequire github.com/sparkwing-dev/sparkwing v1.2.3\n//"
				body += strings.Repeat("x", maxMetadataBytes-len(body))
				writeRepoFile(t, filepath.Join(dir, "go.mod"), body)
				writeRepoFile(t, filepath.Join(dir, "go.sum"), strings.Repeat("x", maxMetadataBytes+1))
			} else {
				body := `{"dependencies":{"@sparkwing/sdk":"1.2.3"},"description":"` + strings.Repeat("x", maxMetadataBytes+1) + `"}`
				writeRepoFile(t, filepath.Join(dir, "package.json"), body)
			}
			report := gatherSDKPinWith(Info{Project: InfoProject{Found: true, SparkwingDir: dir}}, sdk, engine.ReadPin, engine.VersionForPin)
			if report.Runs != "v1.2.3" || report.Refusal != "" {
				t.Fatalf("valid SDK inputs refused: %+v", report)
			}
		})
	}
}

func TestPipelineEngineSwitchRechecksPolicyInSelectedExecutable(t *testing.T) {
	command, selected, output, calls := engineSwitchFixture(t, engine.GoSDK)
	command.current = selected.Executable
	resolved := 0
	denial := &apierr.Error{Code: apierr.VersionRevoked, Message: "signed security reason", Hint: engine.UpgradeCommand}
	command.resolve = func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error) {
		resolved++
		if resolved == 1 {
			return selected, nil
		}
		return engine.Resolved{}, denial
	}
	for i := range 2 {
		err := command.run(t.Context(), "/checkout/.sparkwing", engine.GoSDK, "auto", nil, nil)
		if i == 0 && err != nil || i == 1 && !errors.Is(err, denial) {
			t.Fatalf("policy check %d: %v", i, err)
		}
	}
	if resolved != 2 || *calls != 0 || output.Len() != 0 {
		t.Fatalf("policy bypass or recursive exec: checks=%d exec=%d output=%q", resolved, *calls, output.String())
	}
}

func TestPipelineEngineSwitchNeverFallsBackAfterResolverDenial(t *testing.T) {
	for _, code := range []apierr.Code{apierr.VersionRevoked, apierr.VersionUnsupported, apierr.Unavailable, apierr.IntegrityFailure} {
		t.Run(string(code), func(t *testing.T) {
			command, _, output, calls := engineSwitchFixture(t, engine.GoSDK)
			denial := &apierr.Error{Code: code, Message: "exact release unavailable", Hint: engine.UpgradeCommand}
			resolutions := 0
			command.resolve = func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error) {
				resolutions++
				return engine.Resolved{}, denial
			}
			if err := command.run(t.Context(), "/checkout/.sparkwing", engine.GoSDK, "auto", nil, nil); !errors.Is(err, denial) || resolutions != 1 || *calls != 0 || output.Len() != 0 {
				t.Fatalf("resolver denial changed: %v resolve=%d exec=%d output=%q", err, resolutions, *calls, output.String())
			}
		})
	}
}

func TestPipelineEngineSwitchDisplaysSupportNoticesAndContinues(t *testing.T) {
	command, selected, output, calls := engineSwitchFixture(t, engine.GoSDK)
	selected.Notices = []engine.Notice{{Kind: engine.NoticeSupportEnded, Reason: "support window ended", Hint: "consider sparkwing upgrade"}, {Kind: engine.NoticeIndexFallback, Reason: "last verified policy retained"}}
	command.resolve = func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error) {
		return selected, nil
	}
	if err := command.run(t.Context(), "/checkout/.sparkwing", engine.GoSDK, "auto", nil, nil); err != nil || *calls != 1 || output.String() != "warning: support window ended\nhint: consider sparkwing upgrade\nwarning: last verified policy retained\n" {
		t.Fatalf("notices changed execution: %v exec=%d output=%q", err, *calls, output.String())
	}
}

func TestPipelineEngineSwitchCancellationAtEachBoundaryStopsExecution(t *testing.T) {
	for _, boundary := range []string{"pin", "version", "resolve"} {
		t.Run(boundary, func(t *testing.T) {
			command, selected, output, calls := engineSwitchFixture(t, engine.GoSDK)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			visited := []string{}
			visit := func(name string) {
				if ctx.Err() != nil {
					t.Fatalf("continued after %s cancellation", boundary)
				}
				visited = append(visited, name)
				if name == boundary {
					cancel()
				}
			}
			command.readPin = func(string, engine.SDKKind) (engine.Pin, error) { visit("pin"); return selected.Identity.Pin, nil }
			command.versionFor = func(engine.Pin) (string, error) { visit("version"); return selected.Identity.Version, nil }
			command.resolve = func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error) {
				visit("resolve")
				return selected, nil
			}
			if err := command.run(ctx, "/checkout/.sparkwing", engine.GoSDK, "auto", nil, nil); !errors.Is(err, context.Canceled) || *calls != 0 || output.Len() != 0 || visited[len(visited)-1] != boundary {
				t.Fatalf("cancellation changed: %v exec=%d visited=%q", err, *calls, visited)
			}
		})
	}
}

func TestPipelineEngineSwitchRejectsIncompleteOrDifferentSelection(t *testing.T) {
	for name, mutate := range map[string]func(*engine.Resolved){
		"release":  func(r *engine.Resolved) { r.Identity.Version = "v1.2.2" },
		"pin":      func(r *engine.Resolved) { r.Identity.Pin.InputsDigest = "other-inputs" },
		"sdk":      func(r *engine.Resolved) { r.Identity.Pin.SDK = engine.TypeScriptSDK },
		"manifest": func(r *engine.Resolved) { r.Identity.ManifestDigest = "" },
		"asset":    func(r *engine.Resolved) { r.AssetDigest = "" },
		"relative": func(r *engine.Resolved) { r.Executable = "pipeline-engine" },
		"target":   func(r *engine.Resolved) { r.Target.Binary = releaseasset.SparkwingRunner },
		"platform": func(r *engine.Resolved) { r.Target.GOOS = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			command, selected, output, calls := engineSwitchFixture(t, engine.GoSDK)
			mutate(&selected)
			command.resolve = func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error) {
				return selected, nil
			}
			var failure *apierr.Error
			if err := command.run(t.Context(), "/checkout/.sparkwing", engine.GoSDK, "auto", nil, nil); !errors.As(err, &failure) || failure.Code != apierr.ProtocolMismatch || *calls != 0 || output.Len() != 0 {
				t.Fatalf("invalid selection reached execution: %v exec=%d output=%q", err, *calls, output.String())
			}
		})
	}
}

func TestPipelineEngineSwitchFailureStopsBeforeExecution(t *testing.T) {
	for _, boundary := range []string{"mode", "pin", "version", "resolve", "notice", "context", "current", "selected"} {
		t.Run(boundary, func(t *testing.T) {
			command, selected, _, calls := engineSwitchFixture(t, engine.GoSDK)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("boundary unavailable")
			mode := "auto"
			switch boundary {
			case "mode":
				mode = "unknown"
			case "pin":
				command.readPin = func(string, engine.SDKKind) (engine.Pin, error) { return engine.Pin{}, failure }
			case "version":
				command.versionFor = func(engine.Pin) (string, error) { return "", failure }
			case "resolve":
				command.resolve = func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error) {
					return engine.Resolved{}, failure
				}
			case "notice":
				selected.Notices = []engine.Notice{{Kind: engine.NoticeSupportEnded, Reason: "old release continues"}}
				command.resolve = func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error) {
					return selected, nil
				}
				command.diagnostics = failingEngineWriter{}
			case "context":
				cancel()
			case "current":
				command.current += "-missing"
			case "selected":
				selected.Executable += "-missing"
				command.resolve = func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error) {
					return selected, nil
				}
			}
			if err := command.run(ctx, "/checkout/.sparkwing", engine.GoSDK, mode, nil, nil); err == nil || *calls != 0 {
				t.Fatalf("boundary %s: %v exec=%d", boundary, err, *calls)
			}
		})
	}
}

type failingEngineWriter struct{}

func (failingEngineWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func FuzzPipelineEngineSwitchCannotSubstitutePin(f *testing.F) {
	f.Add("v1.2.2", "v9.9.9", "forged-inputs")
	f.Add("v1.2.3", "v1.2.3", "pin-inputs")
	f.Fuzz(func(t *testing.T, release, ambient, digest string) {
		command, selected, _, calls := engineSwitchFixture(t, engine.GoSDK)
		selected.Identity.Version, selected.Identity.Pin.InputsDigest = release, digest
		command.resolve = func(context.Context, engine.Pin, releaseasset.Target, toolchain.Mode) (engine.Resolved, error) {
			return selected, nil
		}
		err := command.run(t.Context(), "/checkout/.sparkwing", engine.GoSDK, "auto", []string{"run", "build"}, []string{"SPARKWING_TOOLCHAIN_ACTIVE=" + ambient})
		valid := release == "v1.2.3" && digest == "pin-inputs"
		if valid != (err == nil) || valid != (*calls == 1) {
			t.Fatalf("engine substitution: valid=%v exec=%d err=%v", valid, *calls, err)
		}
	})
}

func TestSDKPinSummaryUsesExactEngineWithoutCacheGuess(t *testing.T) {
	for _, installed := range []string{"v1.2.2", "v1.2.3", "v9.9.9", "(devel)"} {
		info := Info{Version: parseInfoVersion(installed), Binary: "/ambient", Project: InfoProject{Found: true, SparkwingDir: "/checkout/.sparkwing"}}
		readPin := func(string, engine.SDKKind) (engine.Pin, error) {
			return engine.Pin{SDK: engine.GoSDK, Version: "v1.2.3"}, nil
		}
		summary := gatherSDKPinWith(info, engine.GoSDK, readPin, func(engine.Pin) (string, error) { return "v1.2.3", nil })
		if summary == nil || summary.Runs != "v1.2.3" || summary.Switches != (installed != "v1.2.3") || summary.RunsFrom != "" || !strings.Contains(sdkPinLine(summary), "verified engine v1.2.3") {
			t.Fatalf("inaccurate pin summary for %s: %+v", installed, summary)
		}
	}
}

func TestSDKPinSummaryPreservesReadAndVersionErrors(t *testing.T) {
	for _, boundary := range []string{"pin", "version"} {
		failure := errors.New("pin cannot select a published engine")
		readPin := func(string, engine.SDKKind) (engine.Pin, error) {
			if boundary == "pin" {
				return engine.Pin{}, failure
			}
			return engine.Pin{SDK: engine.TypeScriptSDK, Version: "1.2.3"}, nil
		}
		versionFor := func(engine.Pin) (string, error) { return "", failure }
		summary := gatherSDKPinWith(Info{Project: InfoProject{Found: true, SparkwingDir: "/checkout/.sparkwing"}}, engine.TypeScriptSDK, readPin, versionFor)
		if summary == nil || summary.Runs != "" || summary.Switches || sdkPinLine(summary) != failure.Error() {
			t.Fatalf("failed pin appeared runnable: %+v", summary)
		}
	}
}
