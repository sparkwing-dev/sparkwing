//go:build !windows

package cluster

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestRunnerSignalExit(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_SIGNAL_HELPER") == "1" {
		command, mode := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
		url, credentials := "https://controller.example", runnerCredentials(t)
		cli := RunnerCLI{ReadConfig: func(string) (userconfig.Config, error) {
			return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &url, CredentialsDir: &credentials}}, nil
		}, Factory: func(ctx context.Context, _ RunnerOptions) (runners.RuntimeConfig, error) {
			if mode == "bundle" {
				fmt.Println("ready")
				_, err := source.Acquire(ctx, source.Spec{
					RepoKey: "https:fixture", RemoteURL: "https://repo.example/test.git", SHA: strings.Repeat("a", 40), BundleDigest: "sha256:" + strings.Repeat("b", 64),
				}, source.Options{
					Engines:   struct{ source.EngineResolver }{},
					Target:    releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"},
					Workspace: t.TempDir(), Access: signalSourceAccess{},
					Base:      procenv.Base{Local: true, Captured: sourcetest.Environment(t)},
					GitAccess: source.GitAccessOwner, BuildEnvironment: sourcetest.BuildEnvironment,
					BundleTransport: signalRoundTrip(func(req *http.Request) (*http.Response, error) {
						if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
							t.Fatal(err)
						}
						<-req.Context().Done()
						return nil, context.Cause(req.Context())
					}),
				})
				return runners.RuntimeConfig{}, err
			}
			if mode == "credential" || mode == "credential-fill" {
				python, err := exec.LookPath("python3")
				if err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				trigger := "config"
				if mode == "credential-fill" {
					trigger = "credential"
				}
				program := fmt.Sprintf("#!%s\nimport os, signal, sys\nif 'config' in sys.argv: print('fixture-helper')\nif %q in sys.argv:\n os.kill(%d, signal.SIGTERM)\n signal.pause()\nif 'cat-file' in sys.argv: sys.exit(1)\n", python, trigger, os.Getpid())
				if err := os.WriteFile(filepath.Join(bin, "git"), []byte(program), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				fmt.Println("ready")
				_, err = source.Acquire(ctx, source.Spec{
					RepoKey: "https:fixture", RemoteURL: "https://repo.example/test.git", SHA: strings.Repeat("a", 40),
				}, source.Options{
					Engines:   struct{ source.EngineResolver }{},
					Target:    releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"},
					Workspace: t.TempDir(), Access: signalSourceAccess{},
					Base:      procenv.Base{Local: true, Captured: sourcetest.Environment(t)},
					GitAccess: source.GitAccessOwner, BuildEnvironment: sourcetest.BuildEnvironment,
					LookupHost: func(context.Context, string) ([]net.IPAddr, error) {
						return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
					},
				})
				return runners.RuntimeConfig{}, err
			}
			if mode == "dns" {
				fmt.Println("ready")
				_, err := source.Acquire(ctx, source.Spec{
					RepoKey: "https:fixture", RemoteURL: "https://repo.example/test.git", SHA: strings.Repeat("a", 40),
				}, source.Options{
					Engines:   struct{ source.EngineResolver }{},
					Target:    releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"},
					Workspace: t.TempDir(), Access: signalSourceAccess{},
					Base:      procenv.Base{Local: true, Captured: sourcetest.Environment(t)},
					GitAccess: source.GitAccessOwner, BuildEnvironment: sourcetest.BuildEnvironment,
					LookupHost: func(lookup context.Context, _ string) ([]net.IPAddr, error) {
						if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
							t.Fatal(err)
						}
						<-lookup.Done()
						return nil, &net.DNSError{Err: lookup.Err().Error(), Name: "repo.example", UnwrapErr: lookup.Err()}
					},
				})
				return runners.RuntimeConfig{}, err
			}
			if mode == "sdk" {
				dir, sha := sourcetest.Repository(t)
				pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v1.2.3", Manifest: "go.mod", InputsDigest: "sealed-pin"}
				target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"}
				resolved := engine.Resolved{Identity: engineid.Identity{Pin: pin, Version: "v1.2.3", ManifestDigest: "signed-manifest"}, Target: target, Executable: filepath.Join(t.TempDir(), "matched-engine")}
				_, err := source.Acquire(ctx, source.Spec{RepoKey: "local:" + dir, LocalCheckout: dir, SHA: sha}, source.Options{
					Engines: signalSourceEngine{resolved}, Target: target, SDK: engineid.GoSDK,
					Base:    procenv.Base{Local: true, Captured: sourcetest.Environment(t)},
					ReadPin: func(string, engineid.SDKKind) (engineid.Pin, error) { return pin, nil },
					BuildEnvironment: func(kind procenv.Kind, base procenv.Base, rt procenv.Runtime) (procenv.Env, error) {
						if kind == procenv.Install {
							fmt.Println("ready")
							if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
								t.Fatal(err)
							}
							<-ctx.Done()
						}
						return sourcetest.BuildEnvironment(kind, base, rt)
					},
				})
				return runners.RuntimeConfig{}, err
			}

			if mode == "checkout" {
				python, err := exec.LookPath("python3")
				if err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				// safety: signaling from Git prevents cancellation at the acquisition entry guard.
				program := fmt.Sprintf("#!%s\nimport os, signal\nos.kill(%d, signal.SIGTERM)\nsignal.pause()\n", python, os.Getpid())
				if err := os.WriteFile(filepath.Join(bin, "git"), []byte(program), 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				fmt.Println("ready")
				_, err = source.Acquire(ctx, source.Spec{RepoKey: "local:fixture", LocalCheckout: t.TempDir()}, source.Options{
					Engines:   struct{ source.EngineResolver }{},
					Target:    releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: "linux", GOARCH: "amd64"},
					Base:      procenv.Base{Local: true, Captured: sourcetest.Environment(t)},
					GitAccess: source.GitAccessOwner, BuildEnvironment: sourcetest.BuildEnvironment,
				})
				return runners.RuntimeConfig{}, err
			}
			if mode == "unrelated" || mode == "unrelated-deadline" {
				cancellation := context.Canceled
				if mode == "unrelated-deadline" {
					cancellation = context.DeadlineExceeded
				}
				return runners.RuntimeConfig{}, fmt.Errorf("real runtime failure: %w", cancellation)
			}
			if mode != "loop" {
				fmt.Println("ready")
				<-ctx.Done()
				if mode == "canceled" {
					return runners.RuntimeConfig{}, errors.Join(context.Cause(ctx), fmt.Errorf("real runtime failure: %w", context.Canceled))
				}
				if mode == "deadline" {
					return runners.RuntimeConfig{}, errors.Join(context.Cause(ctx), context.DeadlineExceeded)
				}
				if mode == "empty-failure" {
					return runners.RuntimeConfig{}, errors.Join(context.Cause(ctx), fmt.Errorf("real runtime failure: %w", emptyRunnerError{}))
				}
				if mode == "mixed-failure" {
					return runners.RuntimeConfig{}, errors.Join(context.Cause(ctx), context.Canceled, errors.New("real runtime failure"))
				}
				if mode == "failure" {
					return runners.RuntimeConfig{}, errors.Join(context.Cause(ctx), errors.New("real runtime failure"))
				}
				return runners.RuntimeConfig{}, fmt.Errorf("startup interrupted: %w", context.Cause(ctx))
			}
			return cliRuntime(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				fmt.Println("ready")
				<-ctx.Done()
				return controllerwire.ClaimResponse{}, context.Cause(ctx)
			}, cliHost(func(context.Context, controllerwire.ClaimedUnit) error { return nil }), cliVersions{index: cliVersionIndex(t)}), nil
		}}
		os.Args = []string{"sparkwing-runner", command, "--config=/fixture/config.yaml"}
		if command == "warm" {
			os.Args = []string{"sparkwing-runner", command, "--controller=" + url, "--credentials-dir=" + credentials}
		}
		mainWithRuntime("v1.2.3", clusterRuntime{Runner: cli})
		return
	}
	for _, command := range []string{"start", "warm"} {
		for _, mode := range []string{"dns", "sdk", "credential", "credential-fill", "bundle", "loop", "startup", "failure", "mixed-failure", "empty-failure", "unrelated", "unrelated-deadline", "canceled", "deadline", "checkout"} {
			t.Run(command+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerSignalExit$", "--", command, mode)
				cmd.Env = append(os.Environ(), "SPARKWING_TEST_SIGNAL_HELPER=1")
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				waited := false
				t.Cleanup(func() {
					if !waited {
						_ = cmd.Process.Kill()
						<-done
					}
				})
				if mode != "unrelated" && mode != "unrelated-deadline" {
					ready := make(chan string, 1)
					go func() { text, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- text }()
					select {
					case line := <-ready:
						if line != "ready\n" {
							t.Fatalf("runner did not reach runtime: %q", line)
						}
					case <-ctx.Done():
						t.Fatal("runner never became ready")
					}
					if mode != "checkout" && mode != "dns" && mode != "sdk" && mode != "credential" && mode != "credential-fill" && mode != "bundle" {
						if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
							t.Fatal(err)
						}
					}
				}
				select {
				case err := <-done:
					waited = true
					if mode == "failure" || mode == "mixed-failure" || mode == "empty-failure" || mode == "unrelated" || mode == "unrelated-deadline" {
						var exit *exec.ExitError
						if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "real runtime failure") {
							t.Fatalf("real failure lost: %v %s", err, &stderr)
						}
					} else if err != nil {
						t.Fatalf("clean SIGTERM exited unsuccessfully: %v %s", err, &stderr)
					}
				case <-ctx.Done():
					_ = cmd.Process.Kill()
					<-done
					t.Fatal("runner did not finish after SIGTERM")
				}
			})
		}
	}
}

type emptyRunnerError struct{}

func (emptyRunnerError) Error() string   { return "empty failure" }
func (emptyRunnerError) Unwrap() []error { return nil }
func TestRunnerEmptyErrorTreeIsFailure(t *testing.T) {
	if runners.OnlyCancellation(emptyRunnerError{}, errors.New("signal stop")) {
		t.Fatal("empty failure was treated as cancellation")
	}
}

type signalSourceAccess struct{}

func (signalSourceAccess) GitCredential(context.Context, source.Spec) (source.Credential, error) {
	return source.Credential{}, nil
}

func (signalSourceAccess) BundleURL(context.Context, string) (string, error) {
	return "https://8.8.8.8/source.bundle", nil
}

type signalRoundTrip func(*http.Request) (*http.Response, error)

func (r signalRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return r(req) }

type signalSourceEngine struct{ resolved engine.Resolved }

func (r signalSourceEngine) Resolve(context.Context, engineid.Pin, releaseasset.Target) (engine.Resolved, error) {
	return r.resolved, nil
}

func (r signalSourceEngine) Ensure(context.Context, engineid.Identity, releaseasset.Target) (engine.Resolved, error) {
	return r.resolved, nil
}
