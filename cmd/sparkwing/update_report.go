package main

import (
	"context"
	gobuildinfo "debug/buildinfo"
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
	localBuild    bool
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
	Kind            string         `json:"kind"`
	Tool            string         `json:"tool"`
	Target          string         `json:"target"`
	Strategy        string         `json:"strategy"`
	Status          string         `json:"status"`
	Before          updateIdentity `json:"before"`
	After           updateIdentity `json:"after"`
	Digest          string         `json:"sha256,omitempty"`
	Replacement     string         `json:"sdk_replace,omitempty"`
	ResolvedRelease string         `json:"resolved_release,omitempty"`
}

func installedArtifactIdentity(path string) updateIdentity {
	identity := updateIdentity{Path: path}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return identity
	}
	file, err := openUpdateInput(path)
	if err != nil {
		return identity
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return identity
	}
	return artifactIdentityFromFile(file, path)
}

func artifactIdentityFromFile(file *os.File, path string) updateIdentity {
	identity := updateIdentity{Path: path}
	info, err := gobuildinfo.Read(file)
	if err != nil {
		return identity
	}
	version := info.Main.Version
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			identity.Revision = setting.Value
		case "vcs.modified":
			if setting.Value == "true" || setting.Value == "false" {
				dirty := setting.Value == "true"
				identity.Dirty = &dirty
			}
		case "-ldflags":
			if !strings.Contains(setting.Value, "main.Version") {
				continue
			}
			version = ""
			if strings.ContainsAny(setting.Value, "'\"\\") {
				continue
			}
			fields := strings.Fields(setting.Value)
			for i, field := range fields {
				assignment := ""
				if field == "-X" && i+1 < len(fields) {
					assignment = fields[i+1]
				} else if strings.HasPrefix(field, "-X=") {
					assignment = strings.TrimPrefix(field, "-X=")
				}
				if strings.HasPrefix(assignment, "main.Version=") {
					version = strings.TrimPrefix(assignment, "main.Version=")
				}
			}
		}
	}
	if semver.IsValid(version) {
		identity.Version = version
	}
	return identity
}

var (
	updateReleaseAPI     = "https://api.github.com/repos/" + updateRepo
	updateLookupRelease  = lookupUpdateRelease
	updateLookupRevision = lookupUpdateRevision
	updateReadInstalled  = readInstalledUpdateIdentity
	updateReadInvoking   = readInvokingUpdateIdentity
	updateDestination    = resolveUpdateDestination
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

func readInvokingUpdateIdentity() updateIdentity {
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

func resolveUpdateDestination() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path, err
	}
	return resolved, nil
}

func readInstalledUpdateIdentity() updateIdentity {
	path, err := updateDestination()
	if err != nil {
		return updateIdentity{Path: path}
	}
	return installedArtifactIdentity(path)
}

func installedReleaseProvenance(ctx context.Context, identity updateIdentity) (verified, local bool, reason string) {
	if !semver.IsValid(identity.Version) {
		return false, false, "installed version is unknown or cannot be compared"
	}
	if identity.Dirty != nil && *identity.Dirty || !isResolvableModuleVersion(identity.Version) {
		return false, true, "local CLI build cannot be compared as a published release"
	}
	if !validUpdateRevision(identity.Revision) || identity.Dirty == nil {
		return false, false, "installed CLI release provenance is unavailable"
	}
	revision, err := updateLookupRevision(ctx, identity.Version)
	if err != nil {
		return false, false, err.Error()
	}
	if identity.Revision != revision {
		return false, true, "installed CLI commit differs from its published release tag"
	}
	return true, false, ""
}

func resolveUpdateVersion(ctx context.Context, requested string, check bool) (string, error) {
	version := requested
	if version == "" {
		var err error
		version, err = updateFetchLatest(ctx)
		if err != nil {
			return "", fmt.Errorf("update: fetch latest published release: %w", err)
		}
	}
	if !validUpdateVersion(version) {
		return "", errors.New("update: release metadata did not name a canonical release tag")
	}
	if check {
		if err := updateLookupRelease(ctx, version); err != nil {
			return "", err
		}
	}
	return version, nil
}

func updateMetadata(ctx context.Context, path string, value any) error {
	client := &http.Client{Timeout: versionFetchTimeout}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, updateReleaseAPI+path, nil)
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

func lookupUpdateRelease(ctx context.Context, version string) error {
	var release struct {
		Tag   string `json:"tag_name"`
		Draft *bool  `json:"draft"`
	}
	if err := updateMetadata(ctx, "/releases/tags/"+neturl.PathEscape(version), &release); err != nil {
		return err
	}
	if release.Tag != version || release.Draft == nil || *release.Draft {
		return errors.New("release metadata does not identify the requested published release")
	}
	return nil
}

func lookupUpdateRevision(ctx context.Context, version string) (string, error) {
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := updateMetadata(ctx, "/git/ref/tags/"+neturl.PathEscape(version), &ref); err != nil {
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
		if err := updateMetadata(ctx, "/git/tags/"+sha, &ref); err != nil {
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
	body, err := readUpdateModule(path)
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
	return finishUpdateCheck(gatherUpdateCheck(target, requested, force, overrideHold), mode)
}

func gatherUpdateCheck(target, requested string, force, overrideHold bool) updateCheckReport {
	return gatherUpdateCheckForIdentity(target, requested, force, overrideHold, nil)
}

func gatherUpdateCheckForIdentity(target, requested string, force, overrideHold bool, identity *updateIdentity) updateCheckReport {
	report := updateCheckReport{Kind: "update_check", Tool: "sparkwing", Target: target, Strategy: "release", Status: "unknown"}
	ctx, cancel := context.WithTimeout(context.Background(), versionFetchTimeout)
	defer cancel()
	if target == "sdk" {
		dir, err := findSparkwingDir()
		if err != nil {
			report.Reason = "no pipeline SDK module found"
			return report
		}
		report.Installed.Path = filepath.Join(dir, "go.mod")
		sdk, err := readSDKUpdateIdentity(dir)
		if err != nil {
			report.Reason = sdkInspectionReason(err)
			return report
		}
		report.Installed = sdk.Identity
		if sdk.Replacement != "" {
			report.Reason = "SDK replacement prevents release identity comparison"
			return report
		}
	} else {
		if identity != nil {
			report.Installed = *identity
		} else {
			report.Installed = updateReadInstalled()
		}
	}
	version, err := resolveUpdateVersion(ctx, requested, true)
	if err != nil {
		report.Reason = err.Error()
		return report
	}
	report.Available.Version = version
	if target == "cli" {
		hold := resolveVersionHold()
		if hold.Error != "" {
			report.BlockedReason = hold.Error
			report.Reason = "operator CLI version hold could not be established"
			return report
		}
		if hold.Error == "" && hold.Value != "" && exceedsHold(version, hold.Value) && !overrideHold {
			report.BlockedReason = fmt.Sprintf("operator CLI version hold %s prevents this target", hold.Value)
		}
	}
	current := report.Installed.Version
	if !semver.IsValid(current) {
		report.Reason = "installed version is unknown or cannot be compared"
		return report
	}
	if target == "cli" {
		verified, local, reason := installedReleaseProvenance(ctx, report.Installed)
		report.localBuild = local
		if !local && report.BlockedReason == "" && classifyDowngrade(current, version) == downgradeNeedsForce && !force {
			report.BlockedReason = "CLI downgrade requires --force"
		}
		if !verified {
			report.Reason = reason
			return report
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
	if report.Status == "current" {
		report.BlockedReason = ""
	}
	return report
}

func sdkInspectionReason(err error) string {
	switch {
	case errors.Is(err, errUpdateModuleType):
		return "SDK module is not a regular file; inspect the reported path"
	case errors.Is(err, errUpdateModuleSize):
		return "SDK module exceeds the 1 MiB inspection limit; inspect the reported path"
	case errors.Is(err, errUpdateModuleChanged):
		return "SDK module changed during inspection; retry after edits finish"
	case errors.Is(err, os.ErrNotExist):
		return "SDK module file is missing at the reported path"
	case errors.Is(err, os.ErrPermission):
		return "SDK module cannot be read; check permissions at the reported path"
	default:
		return "SDK module could not be parsed or read; inspect the reported path"
	}
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
	if report.Installed.Path != "" {
		fmt.Fprintf(w, "  path:      %s\n", report.Installed.Path)
	}
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
	if result.ResolvedRelease != "" && result.After.Version == "" {
		fmt.Fprintf(w, "  release:   %s\n", result.ResolvedRelease)
	}
	return nil
}
