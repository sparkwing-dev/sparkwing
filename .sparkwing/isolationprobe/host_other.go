//go:build !(linux && (amd64 || arm64))

package isolationprobe

import (
	"context"
	"errors"
	"time"
)

type hostOptions struct {
	where        string
	workDir      string
	podNetNS     string
	own          []string
	peerCanaries []string
}

// safety: Cloud jobs run on Linux amd64 or arm64, so a probe elsewhere reports its host checks as
// unprobed rather than passing them.
func hostProbes(o hostOptions) []Result {
	return []Result{{Item: "C12", Context: o.where, Probe: "host checks", Expect: ExpectBlocked, Outcome: "not Linux", Verdict: NotProbed}}
}

func readNetNS() string { return "" }

func runtimeDeviceBind(string, mount) (string, bool) { return "", false }

func discoverNodeAddress(context.Context, time.Duration) (string, error) {
	return "", errors.New("node discovery needs Linux")
}
