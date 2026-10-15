package nodehost

import (
	"context"
	"errors"
)

func startConfiguredProcess(ctx context.Context, cfg SessionConfig) (*ProtocolSession, error) {
	transport, output, err := cfg.protocol(func() error { return nil })
	if err != nil {
		return nil, err
	}
	process, err := StartProtocolSession(ctx, transport)
	if err == nil {
		err = cfg.hello(ctx, process)
	}
	if err != nil {
		if process != nil {
			err = errors.Join(err, process.Close())
		} else {
			output.close()
		}
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { process.failProtocol(context.Cause(ctx)) })
	revoke := process.cfg.Revoke
	process.cfg.Revoke = func() error { stop(); return revoke() }
	return process, nil
}
