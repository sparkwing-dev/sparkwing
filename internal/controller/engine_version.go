package controller

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

// SignedEngineVersionPolicy reads release metadata and notices through the engine owner.
type SignedEngineVersionPolicy struct {
	Resolver  *engine.Resolver
	contracts engineVersionContracts
}

type engineVersionContracts interface {
	Index(context.Context, *engine.Resolver, time.Time) (engine.IndexResult, error)
	Policy(engine.VerifiedIndex) engine.VersionIndex
	Evaluate(engine.VerifiedIndex, string, time.Time) ([]engineid.Notice, error)
	CompatibilityFor(engineid.VersionInfo) controllerwire.Compatibility
}

type sharedEngineVersionContracts struct{}

func (sharedEngineVersionContracts) Index(ctx context.Context, resolver *engine.Resolver, now time.Time) (engine.IndexResult, error) {
	return resolver.Index(ctx, now)
}

func (sharedEngineVersionContracts) Policy(index engine.VerifiedIndex) engine.VersionIndex {
	return index.Policy()
}

func (sharedEngineVersionContracts) Evaluate(index engine.VerifiedIndex, version string, now time.Time) ([]engineid.Notice, error) {
	return engine.Evaluate(index, version, now)
}

func (sharedEngineVersionContracts) CompatibilityFor(release engineid.VersionInfo) controllerwire.Compatibility {
	return controllerwire.CompatibilityFor(release)
}

func (p SignedEngineVersionPolicy) services() (engineVersionContracts, error) {
	if p.Resolver == nil {
		return nil, versionPolicyUnavailable()
	}
	if p.contracts != nil {
		return p.contracts, nil
	}
	return sharedEngineVersionContracts{}, nil
}

// Compatibility uses the exact verified release without applying execution eligibility.
func (p SignedEngineVersionPolicy) Compatibility(ctx context.Context, version string) (controllerwire.Compatibility, error) {
	if version == "" {
		return controllerwire.Compatibility{}, invalidEngineBody("engine_version is required")
	}
	services, err := p.services()
	if err != nil {
		return controllerwire.Compatibility{}, err
	}
	index, err := services.Index(ctx, p.Resolver, time.Now().UTC())
	if err != nil {
		return controllerwire.Compatibility{}, err
	}
	if index.Index.Development() || index.Index.Offline() {
		return controllerwire.Compatibility{APIMajor: controllerwire.APIMajor, EngineRelease: version}, nil
	}
	for _, release := range services.Policy(index.Index).Versions {
		if release.Version == version {
			return services.CompatibilityFor(release), nil
		}
	}
	return controllerwire.Compatibility{}, &apierr.Error{Code: apierr.NotFound, Message: "engine release is absent from verified metadata"}
}

// CheckRunner applies local execution policy for authenticated local subjects.
func (p SignedEngineVersionPolicy) CheckRunner(ctx context.Context, version string) error {
	if p.Resolver == nil {
		return versionPolicyUnavailable()
	}
	now := time.Now().UTC()
	index, err := p.Resolver.Index(ctx, now)
	if err != nil {
		return err
	}
	_, err = engine.Evaluate(index.Index, version, now)
	return err
}

// Notices retains signed warnings and displays enforcement without hiding persisted run status.
func (p SignedEngineVersionPolicy) Notices(ctx context.Context, version string) ([]engineid.Notice, error) {
	if version == "" {
		return nil, invalidEngineBody("engine version is required")
	}
	services, err := p.services()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	index, err := services.Index(ctx, p.Resolver, now)
	if err != nil {
		return nil, err
	}
	notices, err := services.Evaluate(index.Index, version, now)
	var failure *apierr.Error
	if err != nil && (!errors.As(err, &failure) || (failure.Code != apierr.VersionRevoked && failure.Code != apierr.VersionUnsupported)) {
		return nil, err
	}
	out := append([]engineid.Notice{}, index.Notices...)
	out = append(out, notices...)
	if err != nil {
		out = append(out, engineid.Notice{Kind: engineid.NoticeEnforcement, Version: version, Reason: failure.Message, Hint: failure.Hint})
	}
	return out, nil
}

func versionPolicyUnavailable() error {
	return &apierr.Error{Code: apierr.Unavailable, Message: "engine version policy is unavailable"}
}
