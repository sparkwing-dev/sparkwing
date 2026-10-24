//go:build qatrust

package engine

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

const qaModuleProxy = "https://qa-releases.sparkwing.dev/goproxy"

var qaPin = regexp.MustCompile(`-qa\.[1-9][0-9]*$`)

// QAModuleEnvironment returns the settings a go command needs to resolve a QA SDK pin, to append
// after the caller's environment; it returns nothing for any other pin. goEnv runs go with the given
// arguments in the caller's spawn environment and returns its standard output.
//
// safety: only the QA host serves QA SDK versions and the public checksum database has never seen
// them; go.sum still pins them. Settings come from go env so go env -w, GOPROXY=off and GOPRIVATE
// hold. The host answers 403 for modules it lacks, so "|" falls through on any error.
func QAModuleEnvironment(pin engineid.Pin, goEnv func(args ...string) ([]byte, error)) ([]string, error) {
	if pin.Replacement != "" || !qaPin.MatchString(pin.Version) {
		return nil, nil
	}
	body, err := goEnv("env", "-json", "GOPROXY", "GONOSUMDB", "GOPRIVATE")
	if err != nil {
		return nil, fmt.Errorf("read go module settings: %w", err)
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
	return []string{"GOPROXY=" + qaModuleProxy + "|" + current.GOPROXY, "GONOSUMDB=" + nosumdb + GoModule}, nil
}
