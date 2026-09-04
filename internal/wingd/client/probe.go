package client

import (
	"context"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

const probeTimeout = time.Second

type DaemonInfo struct {
	Socket string

	ProtocolMajor int

	BinaryVersion string

	// StoreSchemaVersion is the runs-store schema the daemon's binary
	// understands, or 0 from a daemon that predates the field.
	StoreSchemaVersion int

	// StoreRequirements names the runs-store schema requirements the
	// daemon's binary understands, or nil from a daemon that predates the
	// field.
	StoreRequirements []string

	// StoreReady reports whether the daemon holds an open handle on the
	// runs store file that is there now. Nil from a daemon that predates
	// the field, where StoreError is empty too.
	StoreReady *bool

	// StoreError is why the daemon's own store handle is unusable, empty
	// when it is usable and from a daemon that predates the field.
	StoreError string

	// APIReady reports whether the daemon serves the controller HTTP API on
	// its api.sock. Nil from a daemon that predates the field.
	APIReady *bool

	// APIError is why that socket is unbound, empty when it is bound and
	// from a daemon that predates the field.
	APIError string

	// ArtifactStoreError is why the daemon serves no artifact routes, empty
	// when it resolved a cache or none is configured.
	ArtifactStoreError string

	Draining bool
}

func Probe(ctx context.Context, sock string) (DaemonInfo, error) {
	nc, err := dial(ctx, sock, probeTimeout)
	if err != nil {
		if u := unreachable(sock, err); u != nil {
			return DaemonInfo{}, u
		}
		return DaemonInfo{}, ErrNoDaemon
	}
	defer func() { _ = nc.Close() }()
	// safety: bound reads because a probe has no lease or reconnect path if the
	// daemon accepts but never responds.
	deadline := time.Now().Add(probeTimeout)
	_ = nc.SetDeadline(deadline)
	cl := &Client{nc: nc, dec: newFrameReader(nc), sock: sock, probe: true, connDeadline: deadline}
	ack, err := cl.handshake("")
	if err != nil {
		return DaemonInfo{}, fmt.Errorf("wingd/client: probe %s: %w", sock, err)
	}
	native := ack.NativeProtocolMajor
	if native == 0 {
		native = ack.ProtocolMajor
	}
	return DaemonInfo{
		Socket:             sock,
		ProtocolMajor:      native,
		BinaryVersion:      ack.BinaryVersion,
		StoreSchemaVersion: ack.StoreSchemaVersion,
		StoreRequirements:  ack.StoreRequirements,
		StoreReady:         ack.StoreReady,
		StoreError:         ack.StoreError,
		APIReady:           ack.APIReady,
		APIError:           ack.APIError,
		ArtifactStoreError: ack.ArtifactStoreError,
		Draining:           ack.Draining,
	}, nil
}

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
	cl := &Client{nc: nc, dec: newFrameReader(nc), sock: sock, probe: true, connDeadline: deadline}
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

const healthProbeRetryPace = 25 * time.Millisecond

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
	cl := &Client{nc: nc, dec: newFrameReader(nc), sock: sock, probe: true, connDeadline: deadline}
	ack, err := cl.handshake("")
	if err != nil {
		return fmt.Errorf("wingd/client: health probe %s: %w", sock, err)
	}
	if ack.ProtocolMajor != wingd.ProtocolMajor {
		return fmt.Errorf("wingd/client: health probe %s: protocol %d is incompatible with %d", sock, ack.ProtocolMajor, wingd.ProtocolMajor)
	}
	return nil
}
