package client

import (
	"context"
	"fmt"
	"time"
)

// probeTimeout bounds a single probe end to end, so one wedged daemon
// cannot stall a caller sweeping every socket on the machine.
const probeTimeout = time.Second

// DaemonInfo is what a probe learned about the daemon answering a socket.
type DaemonInfo struct {
	// Socket is the address that answered.
	Socket string
	// ProtocolMajor is the newest wire protocol major the daemon speaks,
	// not the one it agreed to speak with the probe. It may differ from
	// this build's, which is why a probe reads it rather than assuming it.
	ProtocolMajor int
	// BinaryVersion is the version the daemon reports for its own build.
	// Empty for a daemon that predates the field.
	BinaryVersion string
	// Draining reports that the daemon has stopped admitting and is on its
	// way out.
	Draining bool
}

// Probe dials sock, completes the version handshake, and reports what the
// daemon there says it is. It spawns nothing, takes nothing over, and
// holds no lease, so it is safe to point at a daemon serving a home other
// than this process's -- including one whose protocol major this build
// cannot otherwise talk to, since the handshake ack is read before
// anything is asked of it. It returns [ErrNoDaemon] when nothing is listening
// and [ErrDaemonUnreachable] when the socket could not be reached at all,
// because a probe that cannot look must not report the same absence a probe
// that looked and found nothing reports.
func Probe(ctx context.Context, sock string) (DaemonInfo, error) {
	nc, err := dial(ctx, sock, probeTimeout)
	if err != nil {
		if u := unreachable(sock, err); u != nil {
			return DaemonInfo{}, u
		}
		return DaemonInfo{}, ErrNoDaemon
	}
	defer func() { _ = nc.Close() }()
	// safety: a daemon that accepts but never answers would otherwise hang the caller, which has no lease to abandon and no reconnect to fall back on.
	_ = nc.SetDeadline(time.Now().Add(probeTimeout))
	cl := &Client{nc: nc, dec: newFrameReader(nc), sock: sock}
	ack, err := cl.handshake("")
	if err != nil {
		return DaemonInfo{}, fmt.Errorf("wingd/client: probe %s: %w", sock, err)
	}
	native := ack.NativeProtocolMajor
	if native == 0 {
		native = ack.ProtocolMajor
	}
	return DaemonInfo{
		Socket:        sock,
		ProtocolMajor: native,
		BinaryVersion: ack.BinaryVersion,
		Draining:      ack.Draining,
	}, nil
}
