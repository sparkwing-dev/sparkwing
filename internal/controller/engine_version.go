package controller

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

// SignedEngineVersionPolicy reads release metadata and notices through the engine owner.
type SignedEngineVersionPolicy struct {
	Resolver  *engine.Resolver
	contracts engineVersionContracts
}

type engineVersionContracts interface {
	Index(context.Context, *engine.Resolver, time.Time) (engine.IndexResult, error)
	Evaluate(engine.VerifiedIndex, string, time.Time) ([]engineid.Notice, error)
}

type sharedEngineVersionContracts struct{}

func (sharedEngineVersionContracts) Index(ctx context.Context, resolver *engine.Resolver, now time.Time) (engine.IndexResult, error) {
	return resolver.Index(ctx, now)
}

func (sharedEngineVersionContracts) Evaluate(index engine.VerifiedIndex, version string, now time.Time) ([]engineid.Notice, error) {
	return engine.Evaluate(index, version, now)
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

// notices retains signed warnings and displays enforcement without hiding persisted run status.
func (p SignedEngineVersionPolicy) notices(ctx context.Context, version string) ([]engineid.Notice, error) {
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
