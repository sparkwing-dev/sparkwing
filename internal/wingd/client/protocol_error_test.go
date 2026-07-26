package client

import (
	"errors"
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

// TestProtocolTooOld_DoesNotTellTheOperatorToUpgradeTheCLI is the regression
// test for the advice this error used to give. The client is the pipeline
// binary built from the repo's own pin, so upgrading the CLI on PATH cannot
// change the handshake -- it was the wrong instruction in every recorded
// incident and it cost repeated repairs that did not hold.
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

func TestProtocolTooOld_LabelsUnknownVersionsRatherThanPrintingEmpty(t *testing.T) {
	err := protocolTooOld("", wingwire.HelloAck{ProtocolMajor: 2})
	if strings.Count(err.Error(), "(unknown)") != 2 {
		t.Errorf("both unknown versions should be labeled; got: %s", err.Error())
	}
}
