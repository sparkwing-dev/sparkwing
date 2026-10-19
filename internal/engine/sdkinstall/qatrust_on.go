//go:build qatrust

package sdkinstall

import (
	"context"
	"encoding/json"
	"regexp"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
)

const qaModuleProxy = "https://qa-releases.sparkwing.dev/goproxy"

var qaPin = regexp.MustCompile(`-qa\.[1-9][0-9]*$`)

// safety: only the QA host serves QA SDK versions and the public checksum database has never seen
// them; go.sum still pins them. Settings come from go env so go env -w, GOPROXY=off and GOPRIVATE
// hold. The host answers 403 for modules it lacks, so "|" falls through on any error.
func qaModuleEnvironment(ctx context.Context, dir string, pin engine.Pin, environment []string) ([]string, error) {
	if pin.Replacement != "" || !qaPin.MatchString(pin.Version) {
		return nil, nil
	}
	body, err := installTool(ctx, dir, environment, "go", "env", "-json", "GOPROXY", "GONOSUMDB", "GOPRIVATE")
	if err != nil {
		return nil, err
	}
	var current struct{ GOPROXY, GONOSUMDB, GOPRIVATE string }
	if err := json.Unmarshal(body, &current); err != nil {
		return nil, err
	}
	if current.GOPROXY == "off" {
		return nil, nil
	}
	nosumdb := current.GONOSUMDB
	if nosumdb == "" {
		nosumdb = current.GOPRIVATE
	}
	if nosumdb != "" {
		nosumdb += ","
	}
	return []string{"GOPROXY=" + qaModuleProxy + "|" + current.GOPROXY, "GONOSUMDB=" + nosumdb + engine.GoModule}, nil
}
