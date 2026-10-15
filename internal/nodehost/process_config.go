package nodehost

import (
	"context"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/engineenv"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

const (
	LevelInfo = "info"
	LevelWarn = "warn"
)

var reapTimeout = 10 * time.Second

// Sink receives structured records and masked stdout or stderr.
type Sink interface {
	Text(level, line string)
	Record(rec nodeproto.FrameLog)
}

// SessionConfig supplies invocation identity, environment and output policy.
type SessionConfig struct {
	Validation      nodeproto.ValidationContext
	Engine          nodeproto.EngineIdentity
	Runner          nodeproto.RunnerIdentity
	Binding         *nodeproto.ProcessBinding
	Entry           pipelineentry.Command
	Dir             string
	Env             []string
	Sink            Sink
	Masker          *secrets.Masker
	Grace           time.Duration
	DescribeTimeout time.Duration
	HoldOutput      bool
}

type sessionProcess interface {
	LeaderExited() <-chan struct{}
	Finish(ctx context.Context, grace time.Duration) error
	Terminate(ctx context.Context, grace time.Duration) error
}

func (cfg SessionConfig) protocol(revoke func() error) (ProtocolSessionConfig, *processOutput, error) {
	if len(cfg.Entry) == 0 {
		return ProtocolSessionConfig{}, nil, errors.New("sdk process needs a resolved entry command")
	}
	if cfg.Engine.Version == "" || cfg.Runner.Name == "" || cfg.Runner.Type == "" || cfg.Runner.Labels == nil {
		return ProtocolSessionConfig{}, nil, errors.New("sdk process needs the selected engine version and granted runner identity")
	}
	if cfg.Grace <= 0 {
		cfg.Grace = procgroup.DefaultTerminationGrace
	}
	if cfg.Env == nil {
		cfg.Env = engineenv.Minimal()
	}
	output := newProcessOutput(cfg)
	return ProtocolSessionConfig{
		Command: cfg.Entry, Directory: cfg.Dir, Environment: cfg.Env,
		Log: output.record, Revoke: revoke, output: output, grace: cfg.Grace,
	}, output, nil
}

func (cfg SessionConfig) hello(ctx context.Context, session *ProtocolSession) error {
	duration := cfg.DescribeTimeout
	if duration <= 0 {
		duration = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	var hello nodeproto.HelloResult
	return ask(ctx, session, nodeproto.Request{Op: nodeproto.OpHello, Protocol: nodeproto.Version, Engine: &cfg.Engine, Runner: &cfg.Runner, Binding: cfg.Binding}, &hello)
}
