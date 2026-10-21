package nodehost

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/url"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
)

// NativeProcesses binds native SDK startup to the delegated process's services and cleanup ledger.
type NativeProcesses struct {
	compute     processCompute
	Authorities *ProcessAuthorities
	Masks       *MaskRegistry
	URL         string
	Session     ProtocolSessionConfig
	Log         func(context.Context, string, string, nodeproto.FrameLog) error
}

// Delegate creates fresh service authority and cleanup registrations for one SDK process.
func (p *NativeProcesses) Delegate(ctx context.Context, parent capability.CredentialLookup, spec capability.DelegateSpec, services ProcessServices) (ProcessGrant, error) {
	target, err := url.Parse(p.URL)
	if p.Authorities == nil || p.Masks == nil || p.Masks.masker == nil || err != nil || target.Host == "" || target.User != nil || (target.Scheme != "http" && target.Scheme != "https") {
		return ProcessGrant{}, errors.New("native delegation requires authority, masks and a service URL")
	}
	services.Masks, services.Cleanups = p.Masks, &ProcessCleanups{}
	services.activity = &nativeProcessActivity{}
	issued, revoke, err := p.Authorities.Delegate(ctx, parent, spec, &services)
	if err != nil {
		return ProcessGrant{Revoke: revoke}, err
	}
	return ProcessGrant{Binding: nodeproto.ProcessBinding{
		RunID: spec.Binding.RunID, JobID: services.Job.ID,
		Routes: nodeproto.ProcessRoutes{URL: strings.TrimRight(p.URL, "/"), Bearer: issued.Raw},
	}, Revoke: revoke}, nil
}

// Start launches an attempt with its live delegated services.
func (p *NativeProcesses) Start(ctx context.Context, input NodeInput, activity ExecutorActivity) (ExecutorProcess, error) {
	config, err := p.configuration(ctx, input.RouteBinding, input.processRevoke, activity, input.Accepted)
	if err != nil {
		return nil, err
	}
	return p.compute.start(input.Accepted.RunID, func() (ExecutorProcess, error) { return config.Start(ctx, input, activity) })
}

// StartEvaluation launches an owner closure with its live delegated services.
func (p *NativeProcesses) StartEvaluation(ctx context.Context, input EvaluationInput) (ExecutorProcess, error) {
	config, err := p.configuration(ctx, input.RouteBinding, input.processRevoke, ExecutorActivity{}, input.Accepted)
	if err != nil {
		return nil, err
	}
	return p.compute.start(input.Accepted.RunID, func() (ExecutorProcess, error) { return config.StartEvaluation(ctx, input) })
}

// StartPlanning uses the shared environment and planner's secret-discovery log gate.
func (p *NativeProcesses) StartPlanning(ctx context.Context, input PlanningProcessInput, log func(context.Context, nodeproto.FrameLog) error) (ExecutorProcess, error) {
	config := p.Session
	config.Revoke, config.RunCleanups = nil, nil
	return p.compute.start(input.Run.RunID, func() (ExecutorProcess, error) { return config.StartPlanning(ctx, input, log) })
}

// Computing reports whether SDK work remains live for a run.
func (p *NativeProcesses) Computing(runID string) bool { return p.compute.active(runID) }

func (p *NativeProcesses) configuration(ctx context.Context, binding nodeproto.ProcessBinding, revoke func() error, activity ExecutorActivity, accepted AcceptedRun) (ProtocolSessionConfig, error) {
	config := p.Session
	config.Revoke, config.RunCleanups = revoke, nil
	if p.Authorities == nil || p.Log == nil || revoke == nil || binding.Routes.URL != strings.TrimRight(p.URL, "/") {
		return config, config.failedStartup(errors.New("native startup requires its delegated services, logs and revoker"))
	}
	service, err := p.Authorities.processServices(ctx, binding)
	if err != nil {
		return config, config.failedStartup(err)
	}
	if service.Masks != p.Masks || service.activity == nil {
		return config, config.failedStartup(errors.New("native services differ from the shared host binding"))
	}
	service.activity.set(activity)
	debug, err := acceptedDebug(accepted)
	if err != nil {
		return config, config.failedStartup(err)
	}
	logs := &planningLogs{debug: debug, known: true, masker: service.Masks.masker, sink: func(ctx context.Context, record nodeproto.FrameLog) error {
		return p.Log(ctx, service.RunID, service.Job.ID, record)
	}}
	config.Log = logs.write
	config.output, config.Masks = logs, service.Masks
	config.RunCleanups = func(ctx context.Context, scope CleanupScope) error {
		sink := &nativeCleanupSink{ctx: ctx, logs: logs}
		output := &LineWriter{sink: sink, level: LevelInfo, stream: service.Masks.masker.Stream()}
		scope.Log = output
		err := service.Cleanups.Run(ctx, scope)
		output.Flush()
		return errors.Join(err, sink.err)
	}
	return config, nil
}

func (a *ProcessAuthorities) processServices(ctx context.Context, binding nodeproto.ProcessBinding) (*ProcessServices, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	entry := a.live[sha256.Sum256([]byte(binding.Routes.Bearer))]
	if entry == nil || entry.principal.Capability.Binding.RunID != binding.RunID || entry.services == nil || entry.services.Job.ID != binding.JobID || entry.services.Cleanups == nil || entry.services.Masks == nil || entry.services.Masks.masker == nil || entry.services.lifetime.Err() != nil {
		return nil, processUnauthenticated()
	}
	return entry.services, nil
}

type nativeCleanupSink struct {
	ctx  context.Context
	logs *planningLogs
	err  error
}

type nativeProcessActivity struct {
	mu       sync.RWMutex
	activity ExecutorActivity
}

func (a *nativeProcessActivity) set(activity ExecutorActivity) {
	a.mu.Lock()
	a.activity = activity
	a.mu.Unlock()
}

func (a *nativeProcessActivity) wait() func() {
	a.mu.RLock()
	await := a.activity.Await
	a.mu.RUnlock()
	if await != nil {
		return await()
	}
	return func() {}
}

func (s *nativeCleanupSink) Text(level, line string) {
	if s.err == nil && line != "" {
		s.err = s.logs.write(s.ctx, nodeproto.FrameLog{Level: level, Message: line})
	}
}
