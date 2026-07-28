package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestServedDownLevelDetectsBeingTheOlderSide(t *testing.T) {
	tests := []struct {
		name string
		ack  wingwire.HelloAck
		want bool
	}{
		{
			name: "daemon serving an older client reports a higher native major",
			ack:  wingwire.HelloAck{ProtocolMajor: 1, NativeProtocolMajor: 2},
			want: true,
		},
		{
			name: "daemon on the client's own major is not down-level",
			ack:  wingwire.HelloAck{ProtocolMajor: 2, NativeProtocolMajor: 2},
			want: false,
		},
		{
			name: "daemon predating the native field is never down-level",
			ack:  wingwire.HelloAck{ProtocolMajor: 2},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := servedDownLevel(tt.ack); got != tt.want {
				t.Errorf("servedDownLevel(%+v) = %v, want %v", tt.ack, got, tt.want)
			}
		})
	}
}

// A newer daemon that serves this client down-level is the side that must
// survive, however the two binary versions happen to order.
func TestDownLevelServiceOutranksASupersedingVersion(t *testing.T) {
	ack := wingwire.HelloAck{ProtocolMajor: 1, NativeProtocolMajor: 2, BinaryVersion: "v1.0.0"}
	if !supersedes("(devel)", ack.BinaryVersion) {
		t.Fatal("test premise lost: a dev build no longer supersedes a release")
	}
	if !servedDownLevel(ack) {
		t.Error("a client served below the daemon's native major must not take it over")
	}
}

// The daemon's build stamp is not a resolvable module version, so the
// refusal must never present it, or an upgrade the reader has already
// done, as the lever that fixes this.
func TestProtocolTooOldNamesBothMajorsAndNoPinTarget(t *testing.T) {
	ack := wingwire.HelloAck{
		ProtocolMajor:       wingd.ProtocolMajor + 1,
		NativeProtocolMajor: wingd.ProtocolMajor + 1,
		BinaryVersion:       "v0.22.0-dev+b9ade496",
	}
	err := protocolTooOld(ack)
	if !errors.Is(err, ErrProtocolTooOld) {
		t.Fatalf("error %v does not match the sentinel", err)
	}
	msg := err.Error()
	for _, want := range []string{"v0.22.0-dev+b9ade496", "protocol"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q omits %q", msg, want)
		}
	}
	if strings.Contains(msg, "upgrade sparkwing") || strings.Contains(msg, "pin to") {
		t.Errorf("message %q advises an upgrade the reader may already have done", msg)
	}
}
