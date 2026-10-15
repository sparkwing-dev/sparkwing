package runners

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
)

// SoftwarePolicy is the engine resolver's signed runner-update boundary.
type SoftwarePolicy interface {
	Index(context.Context, time.Time) (engine.IndexResult, error)
	PrepareRunnerUpdate(context.Context, string, releaseasset.Target) (engine.SoftwareUpdate, error)
}

// ManagedRunner checks policy at startup and before new claims. Its resolver
// owns the refresh cache. A new release drains held work before replacement;
// the hosts keep renewing until they finish, including under a revoked version.
type ManagedRunner struct {
	Loop         *Loop
	Versions     SoftwarePolicy
	Installation RunnerInstallation
	Target       releaseasset.Target
	Notices      func([]engine.Notice)
}

var errRunnerUpdate = errors.New("runner software update required")

// Run returns after the existing registration has restarted the verified runner,
// or after the loop stops. It never resumes claims under the replaced version.
func (r ManagedRunner) Run(ctx context.Context) error {
	if r.Loop == nil || r.Versions == nil || r.Target.Binary != releaseasset.SparkwingRunner {
		return errors.New("managed runner requires a loop, signed version resolver and runner target")
	}
	r.Loop.CheckVersion = r.checkVersion
	err := r.Loop.Run(ctx)
	var hostErr *hostFailure
	if errors.As(err, &hostErr) {
		return err
	}
	if !errors.Is(err, errRunnerUpdate) {
		return err
	}
	update, err := r.Versions.PrepareRunnerUpdate(ctx, r.Loop.Config.Version, r.Target)
	if err != nil {
		return fmt.Errorf("runner cannot prepare its verified automatic update: %w", err)
	}
	return ApplySoftwareUpdate(ctx, update, r.Installation)
}

func (r ManagedRunner) checkVersion(ctx context.Context, version string) error {
	now := time.Now()
	index, err := r.Versions.Index(ctx, now)
	if err != nil {
		return err
	}
	notices, policyErr := engine.EvaluateRunner(index.Index, version, now)
	if r.Notices != nil {
		r.Notices(append(index.Notices, notices...))
	}
	if policyErr != nil {
		var denial *apierr.Error
		if !errors.As(policyErr, &denial) || (denial.Code != apierr.VersionRevoked && denial.Code != apierr.VersionUnsupported) {
			return policyErr
		}
		return errors.Join(errRunnerUpdate, policyErr)
	}
	if semver.Compare(index.Index.Policy().Latest, version) > 0 {
		return errRunnerUpdate
	}
	return nil
}

var _ SoftwarePolicy = (*engine.Resolver)(nil)
