package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

type updateIdentity struct {
	Version  string `json:"version,omitempty"`
	Revision string `json:"revision,omitempty"`
	Dirty    *bool  `json:"dirty,omitempty"`
	Path     string `json:"path,omitempty"`
}

type updateCheckReport struct {
	Kind          string         `json:"kind"`
	Tool          string         `json:"tool"`
	Target        string         `json:"target"`
	Strategy      string         `json:"strategy"`
	Status        string         `json:"status"`
	Installed     updateIdentity `json:"installed"`
	Available     updateIdentity `json:"available"`
	Reason        string         `json:"reason,omitempty"`
	BlockedReason string         `json:"blocked_reason,omitempty"`
}

type updateReceipt struct {
	Kind        string         `json:"kind"`
	Tool        string         `json:"tool"`
	Target      string         `json:"target"`
	Strategy    string         `json:"strategy"`
	Status      string         `json:"status"`
	Before      updateIdentity `json:"before"`
	After       updateIdentity `json:"after"`
	Digest      string         `json:"sha256,omitempty"`
	Replacement string         `json:"sdk_replace,omitempty"`
}

var (
	updateReleaseAPI     = "https://api.github.com/repos/" + updateRepo
	updateLookupRelease  = lookupUpdateRelease
	updateLookupRevision = lookupUpdateRevision
	updateReadInstalled  = readInstalledUpdateIdentity
)

func updateIdentityFromVersion(version, path string) updateIdentity {
	id := updateIdentity{Path: path}
	if version != "" && !strings.HasPrefix(version, "(") {
		id.Version = version
	}
	info := parseInfoVersion(version)
	id.Revision = info.VCSRevision
	if info.IsDirty {
		dirty := true
		id.Dirty = &dirty
	}
	return id
}

func readInstalledUpdateIdentity() updateIdentity {
	path, err := os.Executable()
	if err != nil {
		path = ""
	}
	id := updateIdentityFromVersion(installedVersion(), path)
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				id.Revision = setting.Value
			case "vcs.modified":
				if setting.Value == "true" || setting.Value == "false" {
					dirty := setting.Value == "true"
					if dirty || id.Dirty == nil {
						id.Dirty = &dirty
					}
				}
			}
		}
	}
	return id
}

func installedReleaseProvenance(identity updateIdentity) (verified, local bool, reason string) {
	if !semver.IsValid(identity.Version) {
		return false, false, "installed version is unknown or cannot be compared"
	}
	if identity.Dirty != nil && *identity.Dirty || !isResolvableModuleVersion(identity.Version) {
		return false, true, "local CLI build cannot be compared as a published release"
	}
	if identity.Revision == "" || identity.Dirty == nil {
		return false, false, "installed CLI release provenance is unavailable"
	}
	revision, err := updateLookupRevision(identity.Version)
	if err != nil {
		return false, false, err.Error()
	}
	if identity.Revision != revision {
		return false, true, "installed CLI commit differs from its published release tag"
	}
	return true, false, ""
}

func resolveUpdateVersion(requested string, check bool) (string, error) {
	version := requested
	if version == "" {
		var err error
		version, err = updateFetchLatest()
		if err != nil {
			return "", fmt.Errorf("update: fetch latest published release: %w", err)
		}
	}
	if !validUpdateVersion(version) {
		return "", errors.New("update: release metadata did not name a canonical release tag")
	}
	if check {
		if err := updateLookupRelease(version); err != nil {
			return "", err
		}
	}
	return version, nil
}

func updateMetadata(path string, value any) error {
	client := &http.Client{Timeout: versionFetchTimeout}
	request, err := http.NewRequest(http.MethodGet, updateReleaseAPI+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("release metadata request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("release metadata returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxMetadataBytes)).Decode(value); err != nil {
		return fmt.Errorf("read release metadata: %w", err)
	}
	return nil
}

func lookupUpdateRelease(version string) error {
	var release struct {
		Tag   string `json:"tag_name"`
		Draft *bool  `json:"draft"`
	}
	if err := updateMetadata("/releases/tags/"+neturl.PathEscape(version), &release); err != nil {
		return err
	}
	if release.Tag != version || release.Draft == nil || *release.Draft {
		return errors.New("release metadata does not identify the requested published release")
	}
	return nil
}

func lookupUpdateRevision(version string) (string, error) {
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := updateMetadata("/git/ref/tags/"+neturl.PathEscape(version), &ref); err != nil {
		return "", err
	}
	for i := 0; i < 3; i++ {
		if !validUpdateRevision(ref.Object.SHA) {
			return "", errors.New("release tag has no valid commit identity")
		}
		if ref.Object.Type == "commit" {
			return ref.Object.SHA, nil
		}
		if ref.Object.Type != "tag" {
			return "", errors.New("release tag does not reference a commit")
		}
		sha := ref.Object.SHA
		if err := updateMetadata("/git/tags/"+sha, &ref); err != nil {
			return "", err
		}
	}
	return "", errors.New("release tag indirection exceeds the metadata lookup limit")
}

func validUpdateRevision(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

type sdkUpdateIdentity struct {
	Identity    updateIdentity
	Replacement string
}

func readSDKUpdateIdentity(dir string) (sdkUpdateIdentity, error) {
	path := filepath.Join(dir, "go.mod")
	body, err := os.ReadFile(path)
	if err != nil {
		return sdkUpdateIdentity{}, fmt.Errorf("read SDK module: %w", err)
	}
	file, err := modfile.Parse(path, body, nil)
	if err != nil {
		return sdkUpdateIdentity{}, fmt.Errorf("parse SDK module: %w", err)
	}
	result := sdkUpdateIdentity{Identity: updateIdentity{Path: path}}
	for _, require := range file.Require {
		if require.Mod.Path == sdkModulePath {
			result.Identity.Version = require.Mod.Version
		}
	}
	for _, replace := range file.Replace {
		if replace.Old.Path == sdkModulePath {
			result.Replacement = replace.New.Path
			if replace.New.Version != "" {
				result.Replacement += "@" + replace.New.Version
			}
			break
		}
	}
	return result, nil
}

func runUpdateCheck(target, requested string, force, overrideHold bool, mode string) error {
	report := updateCheckReport{Kind: "update_check", Tool: "sparkwing", Target: target, Strategy: "release", Status: "unknown"}
	if target == "sdk" {
		dir, err := findSparkwingDir()
		if err != nil {
			report.Reason = "no pipeline SDK module found"
			return finishUpdateCheck(report, mode)
		}
		sdk, err := readSDKUpdateIdentity(dir)
		if err != nil {
			report.Reason = "cannot read a valid pipeline SDK module"
			return finishUpdateCheck(report, mode)
		}
		report.Installed = sdk.Identity
		if sdk.Replacement != "" {
			report.Reason = "SDK replacement prevents release identity comparison"
			return finishUpdateCheck(report, mode)
		}
	} else {
		report.Installed = updateReadInstalled()
	}
	version, err := resolveUpdateVersion(requested, true)
	if err != nil {
		report.Reason = err.Error()
		return finishUpdateCheck(report, mode)
	}
	report.Available.Version = version
	if target == "cli" {
		if hold := resolveVersionHold(); hold.Value != "" && exceedsHold(version, hold.Value) && !overrideHold && report.Installed.Version != version {
			report.BlockedReason = fmt.Sprintf("operator CLI version hold %s prevents this target", hold.Value)
		}
		if report.BlockedReason == "" && classifyDowngrade(report.Installed.Version, version) == downgradeNeedsForce && !force {
			report.BlockedReason = "CLI downgrade requires --force"
		}
	}
	current := report.Installed.Version
	if !semver.IsValid(current) {
		report.Reason = "installed version is unknown or cannot be compared"
		return finishUpdateCheck(report, mode)
	}
	if target == "cli" {
		verified, _, reason := installedReleaseProvenance(report.Installed)
		if !verified {
			report.Reason = reason
			return finishUpdateCheck(report, mode)
		}
	}
	switch semver.Compare(current, version) {
	case -1:
		report.Status = "update_available"
	case 1:
		report.Status = "ahead"
	default:
		if current != version {
			report.Reason = "version labels do not establish identical release identity"
			break
		}
		if target == "sdk" {
			report.Status = "current"
			break
		}
		report.Available.Revision = report.Installed.Revision
		report.Status = "current"
	}
	return finishUpdateCheck(report, mode)
}

func finishUpdateCheck(report updateCheckReport, mode string) error {
	if err := writeUpdateCheck(os.Stdout, report, mode); err != nil {
		return err
	}
	switch report.Status {
	case "current", "ahead":
		return nil
	case "update_available":
		return exitErrorf(1, "update available for %s", report.Target)
	default:
		return exitErrorf(2, "update check for %s: %s", report.Target, report.Reason)
	}
}

func updateIdentityLabel(identity updateIdentity) string {
	if identity.Version != "" {
		return identity.Version
	}
	if identity.Revision != "" {
		return identity.Revision
	}
	return "unknown"
}

func writeUpdateCheck(w io.Writer, report updateCheckReport, mode string) error {
	if mode == "json" {
		return json.NewEncoder(w).Encode(report)
	}
	if mode == "plain" {
		_, err := fmt.Fprintln(w, report.Status)
		return err
	}
	fmt.Fprintf(w, "sparkwing %s update check\n  installed: %s\n  available: %s\n  status:    %s\n", report.Target, updateIdentityLabel(report.Installed), updateIdentityLabel(report.Available), report.Status)
	if report.Reason != "" {
		fmt.Fprintf(w, "  reason:    %s\n", report.Reason)
	}
	if report.BlockedReason != "" {
		fmt.Fprintf(w, "  blocked:   %s\n", report.BlockedReason)
	}
	return nil
}

func writeUpdateReceipt(w io.Writer, result updateReceipt, mode string) error {
	if mode == "json" {
		return json.NewEncoder(w).Encode(result)
	}
	if mode == "plain" {
		_, err := fmt.Fprintln(w, updateIdentityLabel(result.After))
		return err
	}
	fmt.Fprintf(w, "sparkwing %s %s\n  before:    %s\n  installed: %s\n", result.Target, result.Status, updateIdentityLabel(result.Before), updateIdentityLabel(result.After))
	if result.After.Path != "" {
		fmt.Fprintf(w, "  path:      %s\n", result.After.Path)
	}
	if result.Digest != "" {
		fmt.Fprintf(w, "  sha256:    %s\n", result.Digest)
	}
	if result.Replacement != "" {
		fmt.Fprintf(w, "  replace:   %s\n", result.Replacement)
	}
	return nil
}
