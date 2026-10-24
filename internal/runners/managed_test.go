package runners

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

type unavailableVersions struct{ err error }

func (v unavailableVersions) Index(context.Context, time.Time) (engine.IndexResult, error) {
	return engine.IndexResult{}, v.err
}

func (v unavailableVersions) PrepareRunnerUpdate(context.Context, string, releaseasset.Target) (engine.SoftwareUpdate, error) {
	panic("unverified policy cannot select an update")
}

func TestManagedRunnerRequiresVerifiedPolicyBeforeClaiming(t *testing.T) {
	failure := errors.New("no verified version index")
	runner := ManagedRunner{
		Loop: &Loop{Config: LoopConfig{Version: "v1.2.3", MaxConcurrent: 1}, Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			t.Error("claim before verified policy")
			return controllerwire.ClaimResponse{}, nil
		}), Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected host"); return nil })},
		Versions: unavailableVersions{failure},
		Target:   releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: "linux", GOARCH: "amd64"},
	}
	if err := runner.Run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("Run = %v", err)
	}
}

type managedVersionFixture struct {
	initial, replacement       engine.VerifiedIndex
	update                     engine.SoftwareUpdate
	started, release, finished chan struct{}
	checks                     int
	t                          *testing.T
}

func (v *managedVersionFixture) Index(context.Context, time.Time) (engine.IndexResult, error) {
	v.checks++
	if v.checks == 1 {
		return engine.IndexResult{Index: v.initial}, nil
	}
	<-v.started
	close(v.release)
	return engine.IndexResult{Index: v.replacement}, nil
}

func (v *managedVersionFixture) PrepareRunnerUpdate(context.Context, string, releaseasset.Target) (engine.SoftwareUpdate, error) {
	select {
	case <-v.finished:
	default:
		v.t.Error("prepared replacement before host cleanup finished")
	}
	return v.update, nil
}

func TestManagedRunnerDrainsHeldWorkBeforeVerifiedReplacement(t *testing.T) {
	release := runnerReleaseFixture(t)
	initial, replacement := runnerPolicyFixtures(t)
	policy := &managedVersionFixture{initial: initial, replacement: replacement, update: engine.SoftwareUpdate{CurrentVersion: "v0.10.0", Version: "v0.20.0", Release: release, Required: true}, started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{}), t: t}
	installation := &installationFixture{}
	claims := 0
	runner := ManagedRunner{
		Loop: &Loop{
			Config: LoopConfig{Version: "v0.10.0", MaxConcurrent: 2},
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				claims++
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "private"}}, nil
			}),
			Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
				close(policy.started)
				<-policy.release
				defer close(policy.finished)
				return ctx.Err()
			}),
		},
		Versions: policy, Installation: installation, Target: release.Target(),
	}
	if err := runner.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if claims != 1 {
		t.Fatalf("claimed %d units after policy changed", claims)
	}
	if !reflect.DeepEqual(installation.calls, []string{"apply"}) {
		t.Fatalf("installation: %v", installation.calls)
	}
}

func TestManagedRunnerUpdatesAfterPendingAwardDrains(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := runnerReleaseFixture(t)
		initial, replacement := runnerPolicyFixtures(t)
		policy := &managedVersionFixture{initial: initial, replacement: replacement, update: engine.SoftwareUpdate{CurrentVersion: "v0.10.0", Version: "v0.20.0", Release: release, Required: true}, started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{}), t: t}
		installation := &installationFixture{}
		claims, hosted := 0, 0
		runner := ManagedRunner{
			Loop: &Loop{
				Config: LoopConfig{Version: "v0.10.0", MaxConcurrent: 1, MaxClaims: 1},
				Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					claims++
					close(policy.started)
					<-ctx.Done()
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "private"}}, nil
				}),
				Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error {
					hosted++
					defer close(policy.finished)
					return ctx.Err()
				}),
			},
			Versions: policy, Installation: installation, Target: release.Target(),
		}
		if err := runner.Run(t.Context()); err != nil {
			t.Fatal(err)
		}
		if claims != 1 || hosted != 1 || !reflect.DeepEqual(installation.calls, []string{"apply"}) {
			t.Fatalf("claims=%d hosted=%d installation=%v", claims, hosted, installation.calls)
		}
	})
}

func TestManagedRunnerUpdatesAfterCompletedUnitFailure(t *testing.T) {
	release := runnerReleaseFixture(t)
	initial, replacement := runnerPolicyFixtures(t)
	policy := &managedVersionFixture{initial: initial, replacement: replacement, update: engine.SoftwareUpdate{CurrentVersion: "v0.10.0", Version: "v0.20.0", Release: release, Required: true}, started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{}), t: t}
	installation := &installationFixture{}
	failure := errors.New("source cleanup failed")
	runner := ManagedRunner{
		Loop: &Loop{
			Config: LoopConfig{Version: "v0.10.0", MaxConcurrent: 1}, OnError: func(error) {},
			Controller: claimFunc(func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "private"}}, nil
			}),
			Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error {
				close(policy.started)
				close(policy.finished)
				return failure
			}),
		}, Versions: policy, Installation: installation, Target: release.Target(),
	}
	if err := runner.Run(t.Context()); !errors.Is(err, failure) {
		t.Fatalf("lost completed failure: %v", err)
	}
	if !reflect.DeepEqual(installation.calls, []string{"apply"}) {
		t.Fatalf("required update blocked by completed work: %v", installation.calls)
	}
}

func runnerPolicyFixtures(t *testing.T) (engine.VerifiedIndex, engine.VerifiedIndex) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	published := now.AddDate(0, -1, 0)
	policy := engine.VersionIndex{Format: engine.VersionIndexFormat, PublishedAt: now.Add(-time.Minute), Latest: "v0.10.0", Versions: []engineid.VersionInfo{{Version: "v0.10.0", PublishedAt: published, SupportUntil: published.AddDate(0, 12, 0)}}}
	verify := func() engine.VerifiedIndex {
		body, err := json.Marshal(policy)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := engine.VerifyIndex(engine.SignedIndex{Body: body, Signature: ed25519.Sign(key, body)}, now, []ed25519.PublicKey{public})
		if err != nil {
			t.Fatal(err)
		}
		return verified
	}
	initial := verify()
	policy.PublishedAt = now
	policy.Latest = "v0.20.0"
	policy.Versions = append(policy.Versions, engineid.VersionInfo{Version: "v0.20.0", PublishedAt: published, SupportUntil: published.AddDate(0, 12, 0)})
	policy.Minimum = engine.Restriction{Version: "v0.20.0", Reason: "security fixture", Security: true, AnnouncedAt: now.Add(-time.Minute), EffectiveAt: now.Add(-time.Second)}
	return initial, verify()
}

func TestManagedRunnerPreservesCancellationCauseDuringPendingAwardDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		cause := errors.New("operator shutdown")
		release := runnerReleaseFixture(t)
		initial, replacement := runnerPolicyFixtures(t)
		policy := &managedVersionFixture{initial: initial, replacement: replacement, update: engine.SoftwareUpdate{CurrentVersion: "v0.10.0", Version: "v0.20.0", Release: release, Required: true}, started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{}), t: t}
		installation := &installationFixture{}
		runner := ManagedRunner{
			Loop: &Loop{
				Config: LoopConfig{Version: "v0.10.0", MaxConcurrent: 1, MaxClaims: 1},
				Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					close(policy.started)
					<-ctx.Done()
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "private"}}, nil
				}),
				Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error {
					cancel(cause)
					close(policy.finished)
					return nil
				}),
			},
			Versions: policy, Installation: installation, Target: release.Target(),
		}
		err := runner.Run(ctx)
		if len(installation.calls) != 0 {
			t.Errorf("replacement started after cancellation: %v", installation.calls)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("lost cancellation cause: error=%v, cause=%v", err, cause)
		}
	})
}

func TestManagedRunnerPreservesInvalidPendingAwardFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := runnerReleaseFixture(t)
		initial, replacement := runnerPolicyFixtures(t)
		policy := &managedVersionFixture{initial: initial, replacement: replacement, update: engine.SoftwareUpdate{CurrentVersion: "v0.10.0", Version: "v0.20.0", Release: release, Required: true}, started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{}), t: t}
		installation := &installationFixture{}
		runner := ManagedRunner{
			Loop: &Loop{
				Config: LoopConfig{Version: "v0.10.0", MaxConcurrent: 1, MaxClaims: 1},
				Controller: claimFunc(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					close(policy.started)
					<-ctx.Done()
					close(policy.finished)
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, RunID: "run", Capability: "private"}}, nil
				}),
				Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error {
					t.Error("host accepted invalid award")
					return nil
				}),
			},
			Versions: policy, Installation: installation, Target: release.Target(),
		}
		if err := runner.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "controller award is missing unit identity or authority") {
			t.Fatalf("invalid award failure discarded after update: %v; installation=%v", err, installation.calls)
		}
	})
}
