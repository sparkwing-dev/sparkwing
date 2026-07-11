package client

import (
	"strings"

	"golang.org/x/mod/semver"
)

// versionNewer reports whether client is a strictly newer release than
// daemon. Unparseable or empty versions never compare as newer, so an
// unknown build never triggers a takeover.
func versionNewer(client, daemon string) bool {
	c := canonical(client)
	d := canonical(daemon)
	if c == "" || d == "" {
		return false
	}
	return semver.Compare(c, d) > 0
}

func canonical(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return semver.Canonical(v)
}
