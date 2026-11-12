package engine

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

	strictjson "sigs.k8s.io/json"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// VerifyIndex checks signature and policy invariants before accepting authority.
func VerifyIndex(signed SignedIndex, now time.Time, keys []ed25519.PublicKey) (VerifiedIndex, error) {
	if !releaseasset.ManifestSignedBy(keys, signed.Body, signed.Signature) {
		return VerifiedIndex{}, integrity(fmt.Errorf("version index signature is invalid"))
	}
	var policy VersionIndex
	if err := decodeUniqueJSON(signed.Body, &policy); err != nil {
		return VerifiedIndex{}, integrity(err)
	}
	if err := validatePolicy(policy); err != nil {
		return VerifiedIndex{}, integrity(err)
	}
	return VerifiedIndex{index: policy, signed: SignedIndex{Body: bytes.Clone(signed.Body), Signature: bytes.Clone(signed.Signature)}, verifiedAt: now}, nil
}

func validatePolicy(p VersionIndex) error {
	if p.Format != VersionIndexFormat || p.PublishedAt.IsZero() || !releaseVersion(p.Latest) || len(p.Versions) == 0 {
		return fmt.Errorf("version index has missing or unsupported fields")
	}
	versions := map[string]engineid.VersionInfo{}
	for _, v := range p.Versions {
		if !releaseVersion(v.Version) || v.PublishedAt.IsZero() || v.PublishedAt.After(p.PublishedAt) || !v.SupportUntil.Equal(v.PublishedAt.AddDate(0, limits.ControllerCompatibilityMonths, 0)) {
			return fmt.Errorf("invalid support window for %s", v.Version)
		}
		if _, exists := versions[v.Version]; exists {
			return fmt.Errorf("duplicate version %s", v.Version)
		}
		protocols := map[int]bool{}
		for _, protocol := range v.AcceptedSDKProtocols {
			if protocol < 1 || protocols[protocol] {
				return fmt.Errorf("invalid accepted SDK protocols for %s", v.Version)
			}
			protocols[protocol] = true
		}
		versions[v.Version] = v
	}
	if _, ok := versions[p.Latest]; !ok {
		return fmt.Errorf("latest release is absent from version index")
	}
	sdkSeen := map[string]bool{}
	for _, sdk := range p.SDKs {
		key := string(sdk.Language) + ":" + sdk.Version
		if sdk.Language == "" || strings.TrimSpace(string(sdk.Language)) != string(sdk.Language) || !releaseVersion(sdk.Version) || sdk.Protocol < 1 || !validDigest(sdk.SourceDigest) || sdk.SourceDigest != strings.ToLower(sdk.SourceDigest) || sdkSeen[key] {
			return fmt.Errorf("invalid or duplicate SDK entry %s", key)
		}
		sdkSeen[key] = true
		if sdk.Revoked != nil {
			if sdk.Revoked.Version != sdk.Version {
				return fmt.Errorf("SDK revocation version differs from SDK entry")
			}
			if err := validateRestriction(*sdk.Revoked, VersionIndex{PublishedAt: p.PublishedAt}, false); err != nil {
				return err
			}
		}
	}
	seen := map[string]bool{}
	for _, restriction := range p.Revoked {
		if _, ok := versions[restriction.Version]; !ok || seen[restriction.Version] {
			return fmt.Errorf("unknown or duplicate revocation")
		}
		seen[restriction.Version] = true
		if err := validateRestriction(restriction, p, false); err != nil {
			return err
		}
	}
	if p.Minimum.Version != "" {
		if _, ok := versions[p.Minimum.Version]; !ok {
			return fmt.Errorf("minimum release is absent from version index")
		}
		if err := validateRestriction(p.Minimum, p, true); err != nil {
			return err
		}
	} else if p.Minimum != (Restriction{}) {
		return fmt.Errorf("minimum restriction has no version")
	}
	if _, err := evaluatePolicy(p, p.Latest, p.PublishedAt, UpgradeCommand); err != nil {
		return fmt.Errorf("latest release is prohibited: %w", err)
	}
	return nil
}

func validateRestriction(r Restriction, p VersionIndex, minimum bool) error {
	if !releaseVersion(r.Version) || strings.TrimSpace(r.Reason) == "" || r.AnnouncedAt.IsZero() || r.EffectiveAt.IsZero() || r.EffectiveAt.Before(r.AnnouncedAt) || r.AnnouncedAt.After(p.PublishedAt) {
		return fmt.Errorf("restriction has invalid reason or dates")
	}
	if !r.Security && r.EffectiveAt.Before(r.AnnouncedAt.AddDate(0, 0, 14)) {
		return fmt.Errorf("non-security restriction requires at least two weeks notice")
	}
	for _, v := range p.Versions {
		affected := v.Version == r.Version
		if minimum {
			affected = semver.Compare(v.Version, r.Version) < 0
		}
		if affected && !r.Security && r.EffectiveAt.Before(v.SupportUntil) {
			return fmt.Errorf("restriction within %s support window requires a security reason", v.Version)
		}
	}
	return nil
}

// PolicyRefreshHint names the command that caches a verified version policy for local runs.
const PolicyRefreshHint = "Run `sparkwing update` to cache the verified version policy; controller runners still require one."

// Policy returns an independent policy snapshot.
func (v VerifiedIndex) Policy() VersionIndex {
	p := v.index
	p.Versions = append([]engineid.VersionInfo(nil), p.Versions...)
	for i := range p.Versions {
		p.Versions[i].AcceptedSDKProtocols = slices.Clone(p.Versions[i].AcceptedSDKProtocols)
	}
	p.SDKs = slices.Clone(p.SDKs)
	for i := range p.SDKs {
		if p.SDKs[i].Revoked != nil {
			restriction := *p.SDKs[i].Revoked
			p.SDKs[i].Revoked = &restriction
		}
	}
	p.Revoked = append([]Restriction(nil), p.Revoked...)
	return p
}

// Development reports that the host build disabled release-policy enforcement.
func (v VerifiedIndex) Development() bool { return v.development }

// Offline reports local execution without a cached verified policy.
func (v VerifiedIndex) Offline() bool { return v.offline }

// Evaluate applies signed restrictions without treating age as a revocation.
func Evaluate(index VerifiedIndex, version string, now time.Time) ([]engineid.Notice, error) {
	if index.offline {
		return nil, nil
	}
	// safety: a release newer than the never-refreshed cache cannot be restricted by it.
	if index.localCache && !slices.ContainsFunc(index.index.Versions, func(v engineid.VersionInfo) bool { return v.Version == version }) {
		return []engineid.Notice{{Kind: engineid.NoticePolicyUnavailable, Version: version, Reason: "The cached version policy does not list this release; local-only execution continues without policy enforcement.", Hint: PolicyRefreshHint}}, nil
	}
	return evaluate(index, version, now, UpgradeCommand)
}

// EvaluateRunner uses runner repair guidance while leaving claim renewals to their owner.
func EvaluateRunner(index VerifiedIndex, version string, now time.Time) ([]engineid.Notice, error) {
	notices, err := evaluate(index, version, now, runnerHint)
	var api *apierr.Error
	if errors.As(err, &api) && api.Code == apierr.InvalidArgs && !releaseVersion(version) {
		return nil, invalid(fmt.Sprintf("sparkwing-runner %s is a development build; this controller accepts claims only from signed releases", version), developmentRunnerHint)
	}
	return notices, err
}

const (
	runnerHint            = "Allow the runner to update automatically, then restart its existing service; check connectivity if the update fails."
	developmentRunnerHint = "Install a released sparkwing-runner with `install.sh --runner` and restart its service. Development runners claim only from a local controller or a development controller."
)

func evaluate(index VerifiedIndex, version string, now time.Time, hint string) ([]engineid.Notice, error) {
	if index.development {
		return nil, nil
	}
	if index.index.Format != VersionIndexFormat || len(index.signed.Body) == 0 {
		return nil, &apierr.Error{Code: hosterr.Unavailable, Message: "No verified version policy is available", Hint: "Check connectivity to sparkwing.dev and retry."}
	}
	if !releaseVersion(version) {
		return nil, invalid("invalid release version", "Use an exact release version.")
	}
	return evaluatePolicy(index.index, version, now, hint)
}

func evaluatePolicy(p VersionIndex, version string, now time.Time, hint string) ([]engineid.Notice, error) {
	var notices []engineid.Notice
	found := false
	for _, v := range p.Versions {
		if v.Version != version {
			continue
		}
		found = true
		if !now.Before(v.SupportUntil) {
			until := v.SupportUntil
			notices = append(notices, engineid.Notice{Kind: engineid.NoticeSupportEnded, Version: version, Reason: "The support window has ended; this release remains permitted.", Hint: hint, SupportUntil: &until})
		} else if !now.Before(v.SupportUntil.AddDate(0, -1, 0)) {
			until := v.SupportUntil
			notices = append(notices, engineid.Notice{Kind: engineid.NoticeSupportEnding, Version: version, Reason: "The support window ends within one month.", Hint: hint, SupportUntil: &until})
		}
	}
	if !found {
		return nil, &apierr.Error{Code: hosterr.VersionUnsupported, Message: "Release is not listed in the verified version index", Hint: hint, Details: map[string]any{"version": version}}
	}
	for _, r := range p.Revoked {
		if r.Version != version {
			continue
		}
		if !now.Before(r.EffectiveAt) {
			return nil, restrictionError(hosterr.VersionRevoked, version, r, hint)
		}
		notices = append(notices, restrictionNotice(version, r, hint))
	}
	if r := p.Minimum; r.Version != "" && semver.Compare(version, r.Version) < 0 {
		if !now.Before(r.EffectiveAt) {
			return nil, restrictionError(hosterr.VersionUnsupported, version, r, hint)
		}
		notices = append(notices, restrictionNotice(version, r, hint))
	}
	return notices, nil
}

func restrictionError(code apierr.Code, version string, r Restriction, hint string) *apierr.Error {
	return &apierr.Error{Code: code, Message: fmt.Sprintf("Release %s is blocked: %s", version, r.Reason), Hint: hint, Details: map[string]any{"version": version, "minimum": r.Version, "reason": r.Reason, "security": r.Security}}
}

func restrictionNotice(version string, r Restriction, hint string) engineid.Notice {
	at := r.EffectiveAt
	return engineid.Notice{Kind: engineid.NoticeEnforcement, Version: version, Reason: r.Reason, Hint: hint, EnforceAt: &at}
}

func integrity(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &apierr.Error{Code: hosterr.IntegrityFailure, Message: err.Error(), Hint: "Retry with the official signed release; do not execute unverified bytes."}
}

func decodeUniqueJSON(body []byte, out any) error {
	var document any
	failures, err := strictjson.UnmarshalStrict(body, &document, strictjson.DisallowDuplicateFields)
	if err := errors.Join(err, errors.Join(failures...)); err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
