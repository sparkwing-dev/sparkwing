package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// safety: development policy exemptions carry no signed SDK-to-engine authority.
func (r *Resolver) sdkIndex(ctx context.Context, now time.Time) (IndexResult, error) {
	mapping := r
	config := r.config
	if config.BuildVersion != "" && !releaseBuild(config.BuildVersion) {
		config.BuildVersion = ""
		var err error
		mapping, err = NewResolver(config)
		if err != nil {
			return IndexResult{}, err
		}
	}
	result, err := mapping.Index(ctx, now)
	if err != nil || !result.Index.Offline() {
		return result, err
	}
	config.LocalOnly = false
	mapping, err = NewResolver(config)
	if err != nil {
		return IndexResult{}, err
	}
	result, err = mapping.Index(ctx, now)
	if err != nil {
		if ctx.Err() != nil {
			return IndexResult{}, err
		}
		return IndexResult{}, &apierr.Error{Code: hosterr.Unavailable, Message: err.Error(), Hint: "Run `sparkwing update` once online to cache the verified version policy."}
	}
	return result, nil
}

// Index refreshes signed policy and retains verified fallback without an age cutoff.
func (r *Resolver) Index(ctx context.Context, now time.Time) (IndexResult, error) {
	return r.indexPolicy(ctx, now, true)
}

func (r *Resolver) indexPolicy(ctx context.Context, now time.Time, persist bool) (IndexResult, error) {
	if err := ctx.Err(); err != nil {
		return IndexResult{}, err
	}
	if r.config.BuildVersion != "" && !releaseBuild(r.config.BuildVersion) {
		r.notice.Do(func() { slog.Info("version policy checks are off for a development build") })
		return IndexResult{Index: VerifiedIndex{development: true}}, nil
	}
	r.indexMu.Lock()
	defer r.indexMu.Unlock()
	if err := ctx.Err(); err != nil {
		return IndexResult{}, err
	}
	if r.config.LocalOnly {
		return r.localIndex(now), nil
	}
	if r.index.index.Format == 0 {
		cached, err := r.loadIndex(now)
		if err != nil {
			return IndexResult{}, err
		}
		r.index = cached
	}
	if !r.checkedAt.IsZero() && !now.Before(r.checkedAt) && now.Before(r.checkedAt.Add(limits.VersionIndexRefresh)) {
		return indexResult(r.index, r.indexErr), nil
	}
	candidate, err := r.fetchIndex(ctx, now)
	if err == nil {
		err = checkPolicyAdvance(r.index, candidate)
	}
	if err == nil && persist {
		err = r.saveIndex(candidate)
	}
	if err == nil {
		r.index = candidate
		r.checkedAt = now
		r.indexErr = nil
		return IndexResult{Index: candidate}, nil
	}
	if ctx.Err() != nil {
		return IndexResult{}, err
	}
	// safety: Another process may have persisted a revocation while this resolver was fetching.
	persisted, cacheErr := r.loadIndex(now)
	if cacheErr == nil && persisted.index.Format != 0 && !persisted.index.PublishedAt.Before(r.index.index.PublishedAt) {
		cacheErr = checkPolicyAdvance(r.index, persisted)
		if cacheErr == nil {
			r.index = persisted
		}
	}
	err = errors.Join(err, cacheErr)
	if r.index.index.Format == 0 {
		return IndexResult{}, &apierr.Error{Code: hosterr.Unavailable, Message: fmt.Sprintf("Cannot obtain a verified version policy: %v", err), Hint: "Check connectivity to sparkwing.dev and retry before running the pipeline."}
	}
	r.checkedAt = now
	r.indexErr = err
	return indexResult(r.index, err), nil
}

// safety: a qatrust pre-release enforces the QA policy instead of passing as a development build.
func releaseBuild(version string) bool {
	return buildinfo.IsReleaseVersion(version) || mainBuild(version) || QATrust && releaseVersion(version)
}

// DevelopmentBuildEnv set to 1 lets a launcher run a development build on a development cluster.
const DevelopmentBuildEnv = "SPARKWING_DEVELOPMENT_BUILD"

// RequirePolicyBuild refuses a development build, which skips signed version policy, for a component that runs
// customer work, unless allowed reports the explicit override a development cluster set. An empty override means
// the component has none. An unset version enforces policy, as the resolver does.
func RequirePolicyBuild(component, version, override string, allowed bool) error {
	if version == "" || releaseBuild(version) || allowed {
		return nil
	}
	fix := "deploy a release vX.Y.Z or a main build stamped vX.Y.(Z+1)-0.YYYYMMDDhhmmss-SHA12 (sparkwing run build-images stamps it)"
	if override != "" {
		fix += ", or on a development cluster set " + override
	}
	return fmt.Errorf("%s build %q is a development build, which skips signed version policy, so it refuses customer work: %s",
		component, version, fix)
}

var (
	describeSuffix = regexp.MustCompile(`^-(0|[1-9][0-9]*)-g[0-9a-f]{7,40}$`)
	pseudoSuffix   = regexp.MustCompile(`^-0\.[0-9]{14}-[0-9a-f]{12}$`)
)

// safety: main service versions are absent from the index but must still enforce signed engine policy.
// Released image builders also produce git-describe stamps, which must keep enforcing policy.
func mainBuild(version string) bool {
	if !semver.IsValid(version) || semver.Build(version) != "" {
		return false
	}
	suffix := semver.Prerelease(version)
	if describeSuffix.MatchString(suffix) {
		return buildinfo.IsReleaseVersion(version[:len(version)-len(suffix)])
	}
	if !pseudoSuffix.MatchString(suffix) {
		return false
	}
	base, err := module.PseudoVersionBase(version)
	if err != nil || !buildinfo.IsReleaseVersion(base) {
		return false
	}
	_, err = module.PseudoVersionTime(version)
	return err == nil
}

func (r *Resolver) localIndex(now time.Time) IndexResult {
	cached, err := r.loadIndex(now)
	if err == nil && cached.index.Format != 0 {
		err = checkPolicyAdvance(r.index, cached)
		if err == nil {
			r.index = cached
		}
	}
	if r.index.index.Format == 0 {
		notice := engineid.Notice{Kind: engineid.NoticePolicyUnavailable, Reason: "No verified version policy is cached; local-only execution continues without policy enforcement.", Hint: PolicyRefreshHint}
		if err != nil {
			notice.Reason += fmt.Sprintf(" Cached policy could not be read: %v", err)
		}
		return IndexResult{Index: VerifiedIndex{offline: true}, Notices: []engineid.Notice{notice}}
	}
	result := indexResult(r.index, errors.Join(fmt.Errorf("local-only execution uses cached policy without a network refresh"), err))
	for i := range result.Notices {
		result.Notices[i].Hint = PolicyRefreshHint
	}
	result.Index.localCache = true
	return result
}

func (r *Resolver) fetchIndex(ctx context.Context, now time.Time) (VerifiedIndex, error) {
	body, err := r.fetch(ctx, VersionIndexURL, false)
	if err != nil {
		return VerifiedIndex{}, err
	}
	signature, err := r.fetch(ctx, VersionIndexSignatureURL, false)
	if err != nil {
		return VerifiedIndex{}, err
	}
	return VerifyIndex(SignedIndex{Body: body, Signature: signature}, now, r.config.TrustedKeys)
}

func checkPolicyAdvance(previous, next VerifiedIndex) error {
	if previous.index.Format == 0 {
		return nil
	}
	before, after := previous.index.PublishedAt, next.index.PublishedAt
	if after.Before(before) || (after.Equal(before) && !bytes.Equal(previous.signed.Body, next.signed.Body)) {
		return fmt.Errorf("refused version policy rollback or changed body at the same publication time")
	}
	records := map[string]engineid.VersionInfo{}
	for _, record := range next.index.Versions {
		records[record.Version] = record
	}
	for _, record := range previous.index.Versions {
		if current, ok := records[record.Version]; !ok || !reflect.DeepEqual(current, record) {
			return fmt.Errorf("version index changed or removed a retained release record")
		}
	}
	for _, old := range previous.index.SDKs {
		found := false
		for _, current := range next.index.SDKs {
			if old.Language == current.Language && old.Version == current.Version {
				found = true
				if old.Protocol != current.Protocol || old.SourceDigest != current.SourceDigest {
					return fmt.Errorf("version index changed a retained SDK entry")
				}
			}
		}
		if !found {
			return fmt.Errorf("version index removed a retained SDK entry")
		}
	}
	return nil
}

func (r *Resolver) loadIndex(now time.Time) (VerifiedIndex, error) {
	dir := filepath.Join(r.config.CacheDirectory, "policy")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return VerifiedIndex{}, nil
	}
	if err != nil {
		return VerifiedIndex{}, err
	}
	var latest VerifiedIndex
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return VerifiedIndex{}, err
		}
		var signed SignedIndex
		if err := json.Unmarshal(body, &signed); err != nil {
			continue
		}
		candidate, err := VerifyIndex(signed, now, r.config.TrustedKeys)
		if err != nil {
			continue
		}
		if latest.index.Format != 0 && candidate.index.PublishedAt.Before(latest.index.PublishedAt) {
			continue
		}
		if err := checkPolicyAdvance(latest, candidate); err != nil {
			return VerifiedIndex{}, integrity(err)
		}
		latest = candidate
	}
	return latest, nil
}

func (r *Resolver) saveIndex(candidate VerifiedIndex) error {
	// safety: Immutable snapshots keep concurrent processes from overwriting a newer policy floor.
	dir := filepath.Join(r.config.CacheDirectory, "policy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	previous, err := r.loadIndex(candidate.verifiedAt)
	if err != nil {
		return err
	}
	if err := checkPolicyAdvance(previous, candidate); err != nil {
		return err
	}
	body, err := json.Marshal(candidate.signed)
	if err != nil {
		return err
	}
	name := candidate.index.PublishedAt.UTC().Format("20060102T150405.000000000") + "-" + digest(candidate.signed.Body) + ".json"
	return atomicWrite(filepath.Join(dir, name), body, 0o600)
}

func atomicWrite(path string, body []byte, mode os.FileMode) (result error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(file.Name()); err != nil && !os.IsNotExist(err) && result == nil {
			result = err
		}
	}()
	if err := file.Chmod(mode); err != nil {
		closeErr := file.Close()
		return fmt.Errorf("set staged mode: %w (close: %v)", err, closeErr)
	}
	if _, err := file.Write(body); err != nil {
		closeErr := file.Close()
		return fmt.Errorf("write staged file: %w (close: %v)", err, closeErr)
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func indexResult(index VerifiedIndex, err error) IndexResult {
	result := IndexResult{Index: index}
	if err != nil {
		result.Fallback = true
		result.Notices = []engineid.Notice{{Kind: engineid.NoticeIndexFallback, Reason: fmt.Sprintf("Using the last verified version policy: %v", err), Hint: "Check connectivity to sparkwing.dev and retry."}}
	}
	return result
}
