//go:build qatrust

package engine

import (
	"net/url"
	"strings"
)

// A qatrust build reads policy and engines only from the QA origin, so a QA
// pre-release cannot be mixed with production assets.
const (
	indexHost                = "qa-releases.sparkwing.dev"
	VersionIndexURL          = "https://qa-releases.sparkwing.dev/versions.json"
	VersionIndexSignatureURL = "https://qa-releases.sparkwing.dev/versions.json.sig"
	ReleaseBase              = "https://qa-releases.sparkwing.dev/releases/download"
	// QATrust reports a qatrust build; production builds never carry the tag.
	QATrust = true
)

func releaseAssetURL(u *url.URL) bool {
	return u.Host == indexHost && strings.HasPrefix(u.Path, "/releases/download/")
}
