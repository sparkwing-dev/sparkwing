package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestMatchingProtocolRejectsAnUnorderedCrossBuildHandshake(t *testing.T) {
	ack := wingwire.HelloAck{ProtocolMajor: wingd.ProtocolMajor, NativeProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "v1.0.0"}
	err := buildMismatch("(devel)", ack)
	if !errors.Is(err, errBuildMismatch) {
		t.Fatalf("buildMismatch error = %v, want errBuildMismatch", err)
	}
	for _, want := range []string{"(devel)", "v1.0.0", "same protocol major"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("build mismatch error %q omits %q", err, want)
		}
	}
}

func TestMatchingProtocolAcceptsTheExactSameBuild(t *testing.T) {
	ack := wingwire.HelloAck{ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: "v1.0.0", BuildIdentity: wingwire.BuildIdentity}
	if err := buildMismatch("v1.0.0", ack); err != nil {
		t.Fatalf("exact build rejected: %v", err)
	}
}

func TestMatchingProtocolRejectsOldOrMissingNonBlockingBuildIdentityAtSameDisplayVersion(t *testing.T) {
	for _, buildIdentity := range []string{"wingwire-v1-liveness", ""} {
		t.Run(buildIdentity, func(t *testing.T) {
			ack := wingwire.HelloAck{
				ProtocolMajor: wingd.ProtocolMajor,
				BinaryVersion: "v1.0.0",
				BuildIdentity: buildIdentity,
			}
			if err := buildMismatch("v1.0.0", ack); !errors.Is(err, errBuildMismatch) {
				t.Fatalf("old build identity error = %v, want errBuildMismatch", err)
			}
		})
	}
}

func TestMatchingProtocolAcceptsDifferentDisplayVersionsWithSameBuildIdentity(t *testing.T) {
	ack := wingwire.HelloAck{
		ProtocolMajor: wingd.ProtocolMajor,
		BinaryVersion: "v0.31.0-dev+e91a2fe1",
		BuildIdentity: wingwire.BuildIdentity,
	}
	if err := buildMismatch("(devel)", ack); err != nil {
		t.Fatalf("same protocol build identity rejected: %v", err)
	}
}

func TestProtocolTooOldNamesBothMajorsAndNoPinTarget(t *testing.T) {
	ack := wingwire.HelloAck{
		ProtocolMajor:       wingd.ProtocolMajor + 1,
		NativeProtocolMajor: wingd.ProtocolMajor + 1,
		BinaryVersion:       "v0.22.0-dev+b9ade496",
	}
	err := protocolTooOld("", ack)
	if !errors.Is(err, errProtocolTooOld) {
		t.Fatalf("error %v does not match the sentinel", err)
	}
	msg := err.Error()
	for _, want := range []string{"v0.22.0-dev+b9ade496", "protocol"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q omits %q", msg, want)
		}
	}
	if strings.Contains(msg, "v0.22.0-dev+b9ade496 or newer") {
		t.Errorf("message %q pins to an unusable dev stamp", msg)
	}
}
