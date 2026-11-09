package engine

import (
	"fmt"
	"slices"
	"time"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

// SelectSDK selects an explicit SDK release or the newest permitted language entry.
func SelectSDK(index VerifiedIndex, language engineid.SDKKind, version string, now time.Time) (SDKRelease, []engineid.Notice, error) {
	if index.offline {
		return SDKRelease{}, nil, &apierr.Error{Code: apierr.Unavailable, Message: "No verified SDK-to-engine mapping is cached", Hint: PolicyRefreshHint}
	}
	var selected SDKRelease
	var notices []engineid.Notice
	for _, sdk := range index.index.SDKs {
		if sdk.Language != language || version != "" && sdk.Version != version {
			continue
		}
		var current []engineid.Notice
		if sdk.Revoked != nil {
			if !now.Before(sdk.Revoked.EffectiveAt) {
				if version != "" {
					return SDKRelease{}, nil, sdkRestrictionError(sdk)
				}
				continue
			}
			notice := restrictionNotice(sdk.Version, *sdk.Revoked, UpgradeCommand)
			notice.SDK = sdk.Language
			notice.Reason = fmt.Sprintf("%s SDK %s: %s", sdk.Language, sdk.Version, notice.Reason)
			current = append(current, notice)
		}
		if selected.Version == "" || semver.Compare(sdk.Version, selected.Version) > 0 {
			selected, notices = sdk, current
		}
	}
	if selected.Version == "" {
		return SDKRelease{}, nil, &apierr.Error{Code: apierr.VersionUnsupported, Message: fmt.Sprintf("No permitted %s SDK %s is listed in the verified version index", language, version), Hint: UpgradeCommand}
	}
	if selected.Revoked != nil {
		restriction := *selected.Revoked
		selected.Revoked = &restriction
	}
	return selected, notices, nil
}

func sdkRestrictionError(sdk SDKRelease) error {
	api := restrictionError(apierr.VersionRevoked, sdk.Version, *sdk.Revoked, UpgradeCommand)
	api.Message = fmt.Sprintf("%s SDK %s is blocked: %s", sdk.Language, sdk.Version, sdk.Revoked.Reason)
	api.Details["sdk"] = sdk.Language
	return api
}

// SelectEngine chooses the newest permitted engine accepting the SDK entry's protocol.
func SelectEngine(index VerifiedIndex, sdk SDKRelease, now time.Time) (string, error) {
	version := ""
	var prohibited error
	for _, engine := range index.index.Versions {
		if !slices.Contains(engine.AcceptedSDKProtocols, sdk.Protocol) {
			continue
		}
		if _, err := Evaluate(index, engine.Version, now); err != nil {
			prohibited = err
			continue
		}
		if version == "" || semver.Compare(engine.Version, version) > 0 {
			version = engine.Version
		}
	}
	if version == "" {
		if prohibited != nil {
			return "", prohibited
		}
		return "", invalid(fmt.Sprintf("No permitted engine accepts %s SDK %s protocol %d", sdk.Language, sdk.Version, sdk.Protocol), UpgradeCommand)
	}
	return version, nil
}

func acceptsSDK(index VerifiedIndex, pin engineid.Pin, version string, now time.Time) ([]engineid.Notice, error) {
	sdkVersion, err := SDKVersionForPin(pin)
	if err != nil {
		return nil, err
	}
	sdk, notices, err := SelectSDK(index, pin.SDK, sdkVersion, now)
	if err != nil {
		return nil, err
	}
	for _, engine := range index.index.Versions {
		if engine.Version == version && slices.Contains(engine.AcceptedSDKProtocols, sdk.Protocol) {
			return notices, nil
		}
	}
	return nil, integrity(fmt.Errorf("engine %s does not accept %s SDK %s protocol %d", version, pin.SDK, sdkVersion, sdk.Protocol))
}
