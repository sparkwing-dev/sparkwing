//go:build !windows

package main

import (
	"os"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestInspectTriggerRunsOnlyTheSelectedEngineWithCapturedEnvironment(t *testing.T) {
	input, deps, entry := triggerInspectionFixture(t)
	if err := os.Mkdir(entry.Directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry.Engine.Executable, []byte(`#!/bin/sh
set -eu
test "$PWD" = "$EXPECTED_CHECKOUT"
test "$#" = 4
test "$1" = pipeline
test "$2" = describe
test "$3" = --pipeline=deploy-prod
test "$4" = --output=json
test -z "${SPARKWING_TOKEN+x}"
test -z "${SPARKWING_NODE_TOKEN+x}"
printf '%s\n' '{"name":"deploy-prod","entrypoint":"Deploy","args":[{"name":"password","type":"string","secret":true}]}'
`), 0o700); err != nil {
		t.Fatal(err)
	}
	selection, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	deps.exec, deps.buildEnv = nil, nil
	closed := false
	deps.close = func(*source.Entry) error { closed = true; return nil }
	described, err := inspectTriggerSource(t.Context(), selection, []string{"EXPECTED_CHECKOUT=" + entry.Directory, "SPARKWING_TOKEN=private-controller-token", "SPARKWING_NODE_TOKEN=private-node-token"}, deps)
	if err != nil || !closed || len(described) != 1 || len(described[0].Args) != 1 || !described[0].Args[0].Secret {
		t.Fatalf("native selected-engine invocation: %+v closed=%v err=%v", described, closed, err)
	}
}
