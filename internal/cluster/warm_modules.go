package cluster

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

const warmSDKModule = "github.com/sparkwing-dev/sparkwing"

const warmModulesOff = "off"

func parseWarmModules(spec, sdkVersion string) ([]string, error) {
	spec = strings.TrimSpace(spec)
	if strings.EqualFold(spec, warmModulesOff) {
		return nil, nil
	}
	if spec == "" {
		if !semver.IsValid(sdkVersion) {
			return nil, nil
		}
		return []string{warmSDKModule + "@" + sdkVersion}, nil
	}

	entries := splitCSV(spec)
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.ContainsAny(entry, " \t") {
			return nil, fmt.Errorf("module %q contains whitespace", entry)
		}
		path, version, hasVersion := strings.Cut(entry, "@")
		if path == "" {
			return nil, fmt.Errorf("module %q has no module path", entry)
		}
		if !hasVersion {
			version = warmDefaultVersion(path, sdkVersion)
		}
		if version == "" {
			return nil, fmt.Errorf("module %q has an empty version", entry)
		}
		if version != "latest" && !semver.IsValid(version) {
			return nil, fmt.Errorf("module %q: %q is neither a semantic version nor \"latest\"", entry, version)
		}
		out = append(out, path+"@"+version)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func warmDefaultVersion(path, sdkVersion string) string {
	if path == warmSDKModule && semver.IsValid(sdkVersion) {
		return sdkVersion
	}
	return "latest"
}
