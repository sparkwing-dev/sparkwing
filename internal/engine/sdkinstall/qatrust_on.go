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

// qaModuleEnvironment lets a pipeline pinned to a QA SDK fetch it from the QA
// host, which alone serves those versions. It reads the effective settings the
// way go does, so go env -w, GOPROXY=off and GOPRIVATE keep their meaning.
// The QA host answers 403 for modules it lacks, so "|" falls through on any
// error. The public checksum database has never seen QA versions; go.sum
// still pins them.
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
