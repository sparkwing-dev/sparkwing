package client

import (
	"strings"

	"golang.org/x/mod/semver"
)

// supersedes reports whether this client's build should replace the
// running daemon. A build from source beats a release outright, in
// either tag order: it may carry unreleased work no release daemon can
// honor, and its tag says nothing about which is the operator's current
// binary. Two builds of the same kind fall back to semver order, which
// still ranks two source builds by the timestamp in their pseudo-
// versions. Versions that do not order -- "(devel)", "(unknown)", an
// empty string, anything unparseable -- never supersede, so an unknown
// build cannot trigger a takeover.
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
		return devBuild(client)
	}
	c := canonical(client)
	d := canonical(daemon)
	if c == "" || d == "" {
		return false
	}
	return semver.Compare(c, d) > 0
}

// devBuild reports whether v names a build from source rather than a
// release: the Go toolchain's "(devel)" stamp, or a VCS-derived version
// carrying a +dirty suffix.
func devBuild(v string) bool {
	return v == "(devel)" || strings.Contains(v, "+dirty")
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
