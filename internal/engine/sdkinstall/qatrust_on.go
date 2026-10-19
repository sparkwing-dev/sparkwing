//go:build qatrust

package sdkinstall

import (
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

const qaModuleProxy = "https://qa-releases.sparkwing.dev/goproxy"

// qaModuleEnvironment puts the QA proxy ahead of the caller's proxies. The QA
// host answers 403 for modules it lacks, so "|" falls through on any error.
// The public checksum database has never seen QA versions; go.sum still pins them.
func qaModuleEnvironment(environment []string) []string {
	proxy, nosumdb := "https://proxy.golang.org,direct", ""
	for _, setting := range environment {
		if value, ok := strings.CutPrefix(setting, "GOPROXY="); ok && value != "" {
			proxy = value
		}
		if value, ok := strings.CutPrefix(setting, "GONOSUMDB="); ok {
			nosumdb = value
		}
	}
	if nosumdb != "" {
		nosumdb += ","
	}
	return []string{"GOPROXY=" + qaModuleProxy + "|" + proxy, "GONOSUMDB=" + nosumdb + engine.GoModule}
}
