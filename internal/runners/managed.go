package runners

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"golang.org/x/mod/semver"
)

// SoftwarePolicy is the engine resolver's signed runner-update boundary.
type SoftwarePolicy interface {
	Index(context.Context, time.Time) (engine.IndexResult, error)
	PrepareRunnerUpdate(context.Context, string, releaseasset.Target) (engine.SoftwareUpdate, error)
}

// ManagedRunner checks signed policy before claims and drains hosts before verified replacement.
type ManagedRunner struct {
	Loop         *Loop
	Versions     SoftwarePolicy
	Installation RunnerInstallation
	Target       releaseasset.Target
	Notices      func([]engineid.Notice)
}

var errRunnerUpdate = errors.New("runner software update required")

// Run returns after the loop stops or the installed runner restarts with its verified replacement.
func (r ManagedRunner) Run(ctx context.Context) error {
	if r.Loop == nil || r.Versions == nil || r.Target.Binary != releaseasset.SparkwingRunner {
		return errors.New("managed runner requires a loop, signed version resolver and runner target")
	}
	r.Loop.CheckVersion = r.checkVersion
	policyErr, completedFailure := r.Loop.run(ctx)
	if !errors.Is(policyErr, errRunnerUpdate) {
		return errors.Join(policyErr, completedFailure)
	}
	update, err := r.Versions.PrepareRunnerUpdate(ctx, r.Loop.Config.Version, r.Target)
	if err != nil {
		return errors.Join(completedFailure, context.Cause(ctx), fmt.Errorf("runner cannot prepare its verified automatic update: %w", err))
	}
	updateErr := applySoftwareUpdate(ctx, update, r.Installation)
	return errors.Join(completedFailure, context.Cause(ctx), updateErr)
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
