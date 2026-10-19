//go:build !qatrust

package engine

import (
	"net/url"
	"strings"
)

// Production builds read signed policy from sparkwing.dev and engines from GitHub releases.
const (
	indexHost                = "sparkwing.dev"
	VersionIndexURL          = "https://sparkwing.dev/versions.json"
	VersionIndexSignatureURL = "https://sparkwing.dev/versions.json.sig"
	ReleaseBase              = "https://github.com/sparkwing-dev/sparkwing/releases/download"
	// QATrust reports a qatrust build; production builds never carry the tag.
	QATrust = false
)

func releaseAssetURL(u *url.URL) bool {
	switch u.Host {
	case "github.com":
		return strings.HasPrefix(u.Path, "/sparkwing-dev/sparkwing/releases/download/")
	case "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	default:
		return false
	}
}
