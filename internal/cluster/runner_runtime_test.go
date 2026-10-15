package cluster

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type cliClaim func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)

func (f cliClaim) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return f(ctx, request)
}

type cliHost func(context.Context, controllerwire.ClaimedUnit) error

func (f cliHost) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	return f(ctx, unit)
}

type cliVersions struct {
	index engine.VerifiedIndex
	err   error
}

func (v cliVersions) Index(context.Context, time.Time) (engine.IndexResult, error) {
	return engine.IndexResult{Index: v.index}, v.err
}
func (v cliVersions) PrepareRunnerUpdate(context.Context, string, releaseasset.Target) (engine.SoftwareUpdate, error) {
	return engine.SoftwareUpdate{}, errors.New("unexpected update")
}

func TestStartAndWarmExecuteTriggerAndJobOnOneManagedLoop(t *testing.T) {
	versions := cliVersions{index: cliVersionIndex(t)}
	for _, command := range []string{"start", "warm"} {
		t.Run(command, func(t *testing.T) {
			controller := "https://controller.example"
			credentials := runnerCredentials(t)
			concurrency := 1
			kinds := []capability.UnitKind{capability.UnitTrigger, capability.UnitNode}
			var hosted []capability.UnitKind
			claims := 0
			cli := RunnerCLI{
				ReadConfig: func(string) (userconfig.Config, error) {
					return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &controller, CredentialsDir: &credentials, MaxConcurrent: &concurrency}}, nil
				},
				Factory: func(_ context.Context, _ RunnerOptions) (*runners.ManagedRunner, error) {
					var loop *runners.Loop
					loop = &runners.Loop{
						Controller: cliClaim(func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
							if !reflect.DeepEqual(request.Kinds, kinds) || request.RunnerVersion != "v1.2.3" {
								t.Errorf("claim: %#v", request)
							}
							kind := kinds[claims]
							claims++
							return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: kind, UnitID: string(kind), RunID: "run", Capability: "private"}}, nil
						}),
						Host: cliHost(func(ctx context.Context, unit controllerwire.ClaimedUnit) error {
							hosted = append(hosted, unit.Unit)
							if len(hosted) == 2 {
								loop.Drain()
							}
							return ctx.Err()
						}),
					}
					return &runners.ManagedRunner{Loop: loop, Versions: versions, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}, nil
				},
			}
			args := []string{"--config=/fixture/config.yaml"}
			if command == "warm" {
				args = []string{"--controller=" + controller, "--credentials-dir=" + credentials, "--max-concurrent=1"}
			}
			if err := cli.Run(t.Context(), command, args, "v1.2.3"); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(hosted, kinds) {
				t.Fatalf("hosted: %v", hosted)
			}
		})
	}
}

func TestRunnerRequiresSignedPolicyBeforeClaiming(t *testing.T) {
	failure := errors.New("signed policy unavailable")
	cli := RunnerCLI{Factory: func(context.Context, RunnerOptions) (*runners.ManagedRunner, error) {
		return &runners.ManagedRunner{Loop: &runners.Loop{
			Controller: cliClaim(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				t.Error("claimed without signed policy")
				return controllerwire.ClaimResponse{}, nil
			}),
			Host: cliHost(func(context.Context, controllerwire.ClaimedUnit) error {
				t.Error("hosted without signed policy")
				return nil
			}),
		}, Versions: cliVersions{err: failure}, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}, nil
	}}
	if err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", "--credentials-dir=" + runnerCredentials(t)}, "v1.2.3"); !errors.Is(err, failure) {
		t.Fatalf("Run = %v", err)
	}
}

func TestRunnerBindsMetricsBeforeClaiming(t *testing.T) {
	cli := RunnerCLI{Factory: func(context.Context, RunnerOptions) (*runners.ManagedRunner, error) {
		loop := &runners.Loop{
			Controller: cliClaim(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				t.Error("claimed before metrics bound")
				return controllerwire.ClaimResponse{}, errors.New("unexpected claim")
			}),
			Host: cliHost(func(context.Context, controllerwire.ClaimedUnit) error { return nil }),
		}
		return &runners.ManagedRunner{Loop: loop, Versions: cliVersions{index: cliVersionIndex(t)}, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner}}, nil
	}}
	err := cli.Run(t.Context(), "warm", []string{"--controller=https://controller.example", "--credentials-dir=" + runnerCredentials(t), "--metrics-addr=invalid:address:port"}, "v1.2.3")
	if err == nil || !strings.Contains(err.Error(), "metrics") {
		t.Fatalf("metrics bind error: %v", err)
	}
}

func cliVersionIndex(t *testing.T) engine.VerifiedIndex {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := releaseauth.TrustedPublicKeys
	releaseauth.TrustedPublicKeys = []string{base64.StdEncoding.EncodeToString(public)}
	t.Cleanup(func() { releaseauth.TrustedPublicKeys = previous })
	now := time.Now().UTC()
	published := now.AddDate(0, -1, 0)
	policy := engine.VersionIndex{Format: engine.VersionIndexFormat, PublishedAt: now, Latest: "v1.2.3", Versions: []engine.VersionInfo{{Version: "v1.2.3", PublishedAt: published, SupportUntil: published.AddDate(0, 12, 0)}}}
	body, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	index, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: ed25519.Sign(private, body)}, now)
	if err != nil {
		t.Fatal(err)
	}
	return index
}
