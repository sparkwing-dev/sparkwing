// Package releaseasset selects and verifies official Sparkwing release assets.
package releaseasset

import (
	"fmt"
	"sort"
)

// Binary names a release executable.
type Binary string

const (
	Sparkwing           Binary = "sparkwing"
	SparkwingCache      Binary = "sparkwing-cache"
	SparkwingController Binary = "sparkwing-controller"
	SparkwingLogs       Binary = "sparkwing-logs"
	SparkwingRunner     Binary = "sparkwing-runner"
	SparkwingWeb        Binary = "sparkwing-web"
)

var supportedPlatforms = map[Binary]map[string]map[string]bool{
	Sparkwing:           releasePlatforms(true),
	SparkwingCache:      releasePlatforms(false),
	SparkwingController: releasePlatforms(false),
	SparkwingLogs:       releasePlatforms(false),
	SparkwingRunner:     releasePlatforms(false),
	SparkwingWeb:        releasePlatforms(false),
}

func releasePlatforms(windows bool) map[string]map[string]bool {
	platforms := map[string]map[string]bool{
		"darwin": {"amd64": true, "arm64": true},
		"linux":  {"amd64": true, "arm64": true},
	}
	if windows {
		platforms["windows"] = map[string]bool{"amd64": true, "arm64": true}
	}
	return platforms
}

// Target identifies one executable in the closed official release matrix.
type Target struct {
	Binary Binary
	GOOS   string
	GOARCH string
}

// Name returns the immutable release asset name for target.
func (target Target) Name() (string, error) {
	operatingSystems, ok := supportedPlatforms[target.Binary]
	if !ok {
		return "", fmt.Errorf("unsupported Sparkwing release binary %q", target.Binary)
	}
	architectures, ok := operatingSystems[target.GOOS]
	if !ok || !architectures[target.GOARCH] {
		return "", fmt.Errorf("%s has no official release asset for %s/%s", target.Binary, target.GOOS, target.GOARCH)
	}
	extension := ""
	if target.GOOS == "windows" {
		extension = ".exe"
	}
	return string(target.Binary) + "-" + target.GOOS + "-" + target.GOARCH + extension, nil
}

// Names returns the complete official release asset set in lexical order.
func Names() []string {
	var names []string
	for binary, operatingSystems := range supportedPlatforms {
		for goos, architectures := range operatingSystems {
			for goarch := range architectures {
				name, err := (Target{Binary: binary, GOOS: goos, GOARCH: goarch}).Name()
				if err != nil {
					panic(err)
				}
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// ParseName resolves an exact member of the official release asset set.
func ParseName(name string) (Target, error) {
	for binary, operatingSystems := range supportedPlatforms {
		for goos, architectures := range operatingSystems {
			for goarch := range architectures {
				target := Target{Binary: binary, GOOS: goos, GOARCH: goarch}
				candidate, err := target.Name()
				if err == nil && candidate == name {
					return target, nil
				}
			}
		}
	}
	return Target{}, fmt.Errorf("%q is not an official Sparkwing release asset", name)
}
