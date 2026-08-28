package client

import (
	"strings"

	"golang.org/x/mod/semver"
)

func supersedes(client, daemon string) bool {
	client = strings.TrimSpace(client)
	daemon = strings.TrimSpace(daemon)
	if client == "" || daemon == "" || client == daemon {
		return false
	}
	if devBuild(client) != devBuild(daemon) {
		return sourceReleaseSupersedes(client, daemon)
	}
	c := canonical(client)
	d := canonical(daemon)
	if c == "" || d == "" {
		return false
	}
	return semver.Compare(c, d) > 0
}

func sourceReleaseSupersedes(client, daemon string) bool {
	clientBase, clientSource := cleanSourceBase(client)
	daemonBase, daemonSource := cleanSourceBase(daemon)
	switch {
	case clientSource && !devBuild(daemon):
		d := canonical(daemon)
		return d != "" && semver.Compare(clientBase, d) >= 0
	case daemonSource && !devBuild(client):
		c := canonical(client)
		return c != "" && semver.Compare(c, daemonBase) > 0
	default:
		return false
	}
}

func cleanSourceBase(v string) (string, bool) {
	if strings.Contains(v, "+dirty") {
		return "", false
	}
	const marker = "-dev+"
	i := strings.Index(v, marker)
	if i < 0 || i+len(marker) == len(v) {
		return "", false
	}
	base := canonical(v[:i])
	if base == "" || semver.Prerelease(base) != "" {
		return "", false
	}
	revision := v[i+len(marker):]
	if len(revision) < 7 || len(revision) > 40 {
		return "", false
	}
	for _, r := range revision {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return "", false
		}
	}
	return base, true
}

func devBuild(v string) bool {
	return v == "(devel)" || strings.Contains(v, "-dev+") || strings.Contains(v, "+dirty")
}

func canonical(v string) string {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return semver.Canonical(v)
}
