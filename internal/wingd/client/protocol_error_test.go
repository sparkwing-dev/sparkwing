package client

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestProtocolTooOld_StaysMatchableBySentinel(t *testing.T) {
	err := protocolTooOld("v0.17.25", wingwire.HelloAck{ProtocolMajor: 2, BinaryVersion: "v0.22.0"})
	if !errors.Is(err, ErrProtocolTooOld) {
		t.Fatalf("wrapped error no longer matches the sentinel: %v", err)
	}
}

func TestProtocolTooOld_NamesBothSidesAndTheRemedy(t *testing.T) {
	err := protocolTooOld("v0.17.25", wingwire.HelloAck{ProtocolMajor: 2, BinaryVersion: "v0.22.0"})
	msg := err.Error()
	for _, want := range []string{"v0.17.25", "v0.22.0", ".sparkwing/go.mod", "SPARKWING_HOME"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q; got: %s", want, msg)
		}
	}
}

func TestProtocolTooOld_DoesNotTellTheOperatorToUpgradeTheCLI(t *testing.T) {
	err := protocolTooOld("v0.17.25", wingwire.HelloAck{ProtocolMajor: 2, BinaryVersion: "v0.22.0"})
	msg := err.Error()
	if strings.Contains(msg, "upgrade sparkwing;") || strings.Contains(msg, "; upgrade sparkwing") {
		t.Errorf("message still instructs a bare CLI upgrade: %s", msg)
	}
	if !strings.Contains(msg, "upgrading the sparkwing CLI does not affect this handshake") {
		t.Errorf("message should rule the CLI out explicitly; got: %s", msg)
	}
}

func TestProtocolTooOld_RaisesToTheDaemonsReleaseForAMajorThisBuildPredates(t *testing.T) {
	err := protocolTooOld("v0.22.0", wingwire.HelloAck{ProtocolMajor: wingwire.ProtocolMajor + 1, BinaryVersion: "v0.40.0"})
	if !strings.Contains(err.Error(), "pin to v0.40.0 or newer") {
		t.Errorf("message should raise the pin to the daemon's release; got: %s", err.Error())
	}
}

func TestProtocolTooOld_AsksForAReleaseByProtocolWhenNeitherSideNamesOne(t *testing.T) {
	err := protocolTooOld("", wingwire.HelloAck{ProtocolMajor: wingwire.ProtocolMajor + 1})
	want := fmt.Sprintf("a release speaking protocol %d", wingwire.ProtocolMajor+1)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("message should ask for %q rather than an empty version; got: %s", want, err.Error())
	}
}

func TestProtocolTooOld_LabelsUnknownVersionsRatherThanPrintingEmpty(t *testing.T) {
	err := protocolTooOld("", wingwire.HelloAck{ProtocolMajor: 2})
	if strings.Count(err.Error(), "(unknown)") != 2 {
		t.Errorf("both unknown versions should be labeled; got: %s", err.Error())
	}
}

func TestProtocolTooOld_AsksForAReleaseByProtocolWhenTheDaemonVersionIsAPrerelease(t *testing.T) {
	err := protocolTooOld("v0.22.0", wingwire.HelloAck{ProtocolMajor: wingwire.ProtocolMajor + 1, BinaryVersion: "v0.22.0-dev+b9ade496"})
	want := fmt.Sprintf("a release speaking protocol %d", wingwire.ProtocolMajor+1)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("message names an unusable prerelease instead of asking for %q; got: %s", want, err.Error())
	}
}
