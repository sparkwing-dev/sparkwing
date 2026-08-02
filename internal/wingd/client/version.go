package client

import (
	"strings"

	"golang.org/x/mod/semver"
)

// supersedes reports whether this client's build should replace the
// running daemon. Exact clean source builds order after the release they
// were built from and before later releases. Opaque and dirty source builds
// remain unordered against releases, so worktree binaries can share a stable
// daemon without racing to replace it. Two builds of the same kind fall back
// to semver order. Versions that do not order -- "(devel)", "(unknown)", an
// empty string, anything unparseable -- never supersede, so an unknown build
// cannot trigger a takeover.
//
// Every rule here is one-directional on purpose. Two builds that each
// superseded the other would drain and respawn each other's daemon for
// as long as both stayed in use, and nothing in [Client.connect] bounds
// that exchange. Identical versions never supersede for the same
// reason: a client must not drain the successor it just brought up,
// which reports the client's own version.
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

// sourceReleaseSupersedes orders an exact clean source build against a
// release. A vX.Y.Z-dev+REV source build contains one useful fact: it was
// built from or after vX.Y.Z. It therefore supersedes that release and any
// older version, while a later release supersedes it. Opaque and dirty builds
// carry no stable ordering evidence and remain unordered.
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

// devBuild reports whether v names a build from source rather than a
// release: the Go toolchain's "(devel)" stamp, an installed clean source
// build carrying -dev+revision, or a VCS-derived version carrying +dirty.
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
