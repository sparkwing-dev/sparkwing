package client

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
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
// that looked and found nothing reports. Its hello declares a health probe,
// so sweeping every socket on the machine keeps no idle daemon alive.
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
	cl := &Client{nc: nc, dec: newFrameReader(nc), sock: sock, probe: true}
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

// ProbeQueue reads one queue snapshot from an already-running daemon without
// entering the spawn/election path. Status and dry-run callers use it when
// even creating the daemon's lock directory would violate their contract.
func ProbeQueue(ctx context.Context, sock string) (wingwire.QueueState, error) {
	nc, err := dial(ctx, sock, probeTimeout)
	if err != nil {
		if u := unreachable(sock, err); u != nil {
			return wingwire.QueueState{}, u
		}
		return wingwire.QueueState{}, ErrNoDaemon
	}
	defer func() { _ = nc.Close() }()
	deadline := time.Now().Add(probeTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = nc.SetDeadline(deadline)
	cl := &Client{nc: nc, dec: newFrameReader(nc), sock: sock, probe: true}
	ack, err := cl.handshake("")
	if err != nil {
		return wingwire.QueueState{}, fmt.Errorf("wingd/client: queue probe %s: %w", sock, err)
	}
	if ack.ProtocolMajor != wingd.ProtocolMajor {
		return wingwire.QueueState{}, fmt.Errorf("wingd/client: queue probe %s: protocol %d is incompatible with %d", sock, ack.ProtocolMajor, wingd.ProtocolMajor)
	}
	qs, terminal, transient := cl.readQueueState()
	if terminal != nil {
		return wingwire.QueueState{}, terminal
	}
	if transient != nil {
		return wingwire.QueueState{}, fmt.Errorf("wingd/client: queue probe %s: %w", sock, transient)
	}
	return qs, nil
}

// healthProbeRetryPace spaces re-dials of one health probe inside its
// caller's deadline, so a probe against a child mid-startup keeps looking
// rather than condemning it on the first unanswered dial.
const healthProbeRetryPace = 25 * time.Millisecond

// HealthProbe asks home's daemon to prove it is serving: one handshake and
// one queue-state round trip -- the same depth [Query] exercises -- on a
// connection whose hello declares a health probe. The daemon answers it
// without advancing its idle clock, so a supervisor can probe on an interval
// far shorter than the idle window and the daemon still idles out when its
// real clients are gone; the supervisor then sees the clean exit and reaps
// the pair. Probing through [Query] instead defeats idle-exit, which is how
// supervised daemons in throwaway homes became unkillable. It spawns
// nothing and takes nothing over, and returns [ErrNoDaemon] when nothing is
// listening and [ErrDaemonUnreachable] when the socket could not be reached
// at all.
//
// One failed exchange is not a failed probe: a child that has won the
// election but not yet bound its socket, or a starved box that lost a
// frame, looks momentarily absent. [Query] absorbed that through its
// connect loop's re-dials; HealthProbe keeps the same tolerance by
// retrying the whole exchange until ctx expires, so its verdict is "the
// daemon did not answer within the caller's probe budget", never "one
// dial failed".
func HealthProbe(ctx context.Context, home string) error {
	sock, err := wingd.SocketPath(home)
	if err != nil {
		return err
	}
	// safety: the retry loop runs until ctx expires, so a caller that set
	// no deadline gets the default probe budget rather than a probe that
	// never returns while the daemon is absent.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, probeTimeout)
		defer cancel()
	}
	for {
		probeErr := healthProbeOnce(ctx, sock)
		if probeErr == nil {
			return nil
		}
		if sleep(ctx, healthProbeRetryPace) != nil {
			return probeErr
		}
	}
}

// healthProbeOnce runs a single dial + handshake + queue-state exchange.
func healthProbeOnce(ctx context.Context, sock string) error {
	nc, err := dial(ctx, sock, probeTimeout)
	if err != nil {
		if u := unreachable(sock, err); u != nil {
			return u
		}
		return ErrNoDaemon
	}
	defer func() { _ = nc.Close() }()
	// safety: a daemon that accepts but never answers must fail the probe
	// within its deadline, not hang the supervisor's watch loop. The
	// caller's ctx deadline is the probe budget when it has one --
	// [probeTimeout] only bounds a caller that set none.
	deadline := time.Now().Add(probeTimeout)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	_ = nc.SetDeadline(deadline)
	cl := &Client{nc: nc, dec: newFrameReader(nc), sock: sock, probe: true}
	if _, err := cl.handshake(""); err != nil {
		return fmt.Errorf("wingd/client: health probe %s: %w", sock, err)
	}
	if _, terminal, transient := cl.readQueueState(); terminal != nil || transient != nil {
		err := terminal
		if err == nil {
			err = transient
		}
		return fmt.Errorf("wingd/client: health probe %s: %w", sock, err)
	}
	return nil
}
