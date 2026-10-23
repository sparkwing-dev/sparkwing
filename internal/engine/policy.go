package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"

	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/releaseauth"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/limits"
	"golang.org/x/mod/semver"
	strictjson "sigs.k8s.io/json"
)

// VerifyIndex checks signature and policy invariants before accepting authority.
func VerifyIndex(signed SignedIndex, now time.Time) (VerifiedIndex, error) {
	keys, err := releaseauth.PublicKeys()
	if err != nil {
		return VerifiedIndex{}, integrity(err)
	}
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
		versions[v.Version] = v
	}
	if _, ok := versions[p.Latest]; !ok {
		return fmt.Errorf("latest release is absent from version index")
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

// Policy returns an independent policy snapshot.
func (v VerifiedIndex) Policy() VersionIndex {
	p := v.index
	p.Versions = append([]engineid.VersionInfo(nil), p.Versions...)
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
	return evaluate(index, version, now, UpgradeCommand)
}

// EvaluateRunner uses runner repair guidance while leaving claim renewals to their owner.
func EvaluateRunner(index VerifiedIndex, version string, now time.Time) ([]engineid.Notice, error) {
	return evaluate(index, version, now, runnerHint)
}

const runnerHint = "Allow the runner to update automatically, then restart its existing service; check connectivity if the update fails."

func evaluate(index VerifiedIndex, version string, now time.Time, hint string) ([]engineid.Notice, error) {
	if index.development {
		return nil, nil
	}
	if index.index.Format != VersionIndexFormat || len(index.signed.Body) == 0 {
		return nil, &apierr.Error{Code: apierr.Unavailable, Message: "No verified version policy is available", Hint: "Check connectivity to sparkwing.dev and retry."}
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
		return nil, &apierr.Error{Code: apierr.VersionUnsupported, Message: "Release is not listed in the verified version index", Hint: hint, Details: map[string]any{"version": version}}
	}
	for _, r := range p.Revoked {
		if r.Version != version {
			continue
		}
		if !now.Before(r.EffectiveAt) {
			return nil, restrictionError(apierr.VersionRevoked, version, r, hint)
		}
		notices = append(notices, restrictionNotice(version, r, hint))
	}
	if r := p.Minimum; r.Version != "" && semver.Compare(version, r.Version) < 0 {
		if !now.Before(r.EffectiveAt) {
			return nil, restrictionError(apierr.VersionUnsupported, version, r, hint)
		}
		notices = append(notices, restrictionNotice(version, r, hint))
	}
	return notices, nil
}

func restrictionError(code apierr.Code, version string, r Restriction, hint string) error {
	return &apierr.Error{Code: code, Message: fmt.Sprintf("Release %s is blocked: %s", version, r.Reason), Hint: hint, Details: map[string]any{"version": version, "minimum": r.Version, "reason": r.Reason, "security": r.Security}}
}

func restrictionNotice(version string, r Restriction, hint string) engineid.Notice {
	at := r.EffectiveAt
	return engineid.Notice{Kind: engineid.NoticeEnforcement, Version: version, Reason: r.Reason, Hint: hint, EnforceAt: &at}
}

func integrity(err error) error {
	return &apierr.Error{Code: apierr.IntegrityFailure, Message: err.Error(), Hint: "Retry with the official signed release; do not execute unverified bytes."}
}

func decodeUniqueJSON(body []byte, out any) error {
	var document any
	failures, err := strictjson.UnmarshalStrict(body, &document, strictjson.DisallowDuplicateFields)
	if err := errors.Join(err, errors.Join(failures...)); err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
