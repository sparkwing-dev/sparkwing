package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

type versionPolicyStatus struct {
	Status      string     `json:"status"`
	Latest      string     `json:"latest,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	Age         string     `json:"age,omitempty"`
	Reason      string     `json:"reason,omitempty"`
}

// safety: local runs never fetch the signed policy, so update is the command that fills the cache they read.
func refreshVersionPolicy(ctx context.Context) versionPolicyStatus {
	resolver, err := engine.NewResolver(engine.Config{})
	if err != nil {
		return versionPolicyStatus{Status: "unavailable", Reason: err.Error()}
	}
	result, err := resolver.Index(ctx, time.Now())
	return describeVersionPolicy(result, err, time.Now())
}

// safety: doctor and update --dry-run stay read-only, so an absent cache is reported without creating it.
func cachedVersionPolicy(ctx context.Context) versionPolicyStatus {
	home, err := paths.DefaultPaths()
	if err != nil {
		return versionPolicyStatus{Status: "unavailable", Reason: err.Error()}
	}
	if _, err := os.Stat(filepath.Join(home.Root, "engines", "policy")); err != nil {
		return versionPolicyStatus{Status: "missing", Reason: engine.PolicyRefreshHint}
	}
	resolver, err := engine.NewResolver(engine.Config{BuildVersion: installedVersion(), LocalOnly: true})
	if err != nil {
		return versionPolicyStatus{Status: "unavailable", Reason: err.Error()}
	}
	result, err := resolver.Index(ctx, time.Now())
	return describeVersionPolicy(result, err, time.Now())
}

func describeVersionPolicy(result engine.IndexResult, err error, now time.Time) versionPolicyStatus {
	switch {
	case err != nil:
		return versionPolicyStatus{Status: "unavailable", Reason: err.Error()}
	case result.Index.Development():
		return versionPolicyStatus{Status: "development", Reason: "version policy checks are off for a development build"}
	case result.Index.Offline():
		return versionPolicyStatus{Status: "missing", Reason: engine.PolicyRefreshHint}
	}
	policy := result.Index.Policy()
	published := policy.PublishedAt.UTC()
	status := versionPolicyStatus{Status: "cached", Latest: policy.Latest, PublishedAt: &published, Age: max(0, now.Sub(published)).Truncate(time.Minute).String()}
	if result.Fallback && len(result.Notices) > 0 {
		status.Status, status.Reason = "stale", result.Notices[0].Reason
	}
	return status
}
