//go:build qatrust

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
)

const qaModuleProxy = "https://qa-releases.sparkwing.dev/goproxy"

var qaPin = regexp.MustCompile(`-qa\.[1-9][0-9]*$`)

// QAModuleEnvironment returns the settings a go command in dir needs to resolve a QA SDK pin,
// appended after environment; it returns nothing for any other pin.
//
// safety: only the QA host serves QA SDK versions and the public checksum database has never seen
// them; go.sum still pins them. Settings come from go env so go env -w, GOPROXY=off and GOPRIVATE
// hold. The host answers 403 for modules it lacks, so "|" falls through on any error.
func QAModuleEnvironment(ctx context.Context, dir string, pin Pin, environment []string) ([]string, error) {
	if pin.Replacement != "" || !qaPin.MatchString(pin.Version) {
		return nil, nil
	}
	command := exec.Command("go", "env", "-json", "GOPROXY", "GONOSUMDB", "GOPRIVATE")
	var stdout, stderr bytes.Buffer
	command.Dir, command.Env, command.Stdout, command.Stderr = dir, environment, &stdout, &stderr
	if err := gotoolchain.Run(ctx, command); err != nil {
		return nil, fmt.Errorf("read go module settings: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var current struct{ GOPROXY, GONOSUMDB, GOPRIVATE string }
	if err := json.Unmarshal(stdout.Bytes(), &current); err != nil {
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
