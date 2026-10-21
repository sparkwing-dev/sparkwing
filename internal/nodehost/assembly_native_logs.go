package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type localLogs struct {
	mu       sync.Mutex
	run      string
	masker   *secrets.Masker
	current  func(context.Context) (capability.Principal, error)
	claims   *nodeClaims
	readNode func(context.Context, controllerwire.NodeRef) (store.NodeRow, error)
	upload   func(context.Context, capability.Principal, blob.LogSubject) (logseg.Uploader, error)
	sinks    map[string]*logseg.Sink
	closed   bool
}

type logHostStore struct {
	NativeHostStore
	logs *localLogs
	unit capability.UnitKind
}

func (s logHostStore) AcceptPlan(ctx context.Context, request store.PlanAcceptance) (store.AcceptedPlan, error) {
	if s.unit == capability.UnitPlan {
		if err := s.logs.close(context.WithoutCancel(ctx)); err != nil {
			return store.AcceptedPlan{}, err
		}
	}
	return s.NativeHostStore.AcceptPlan(ctx, request)
}

func (s logHostStore) AcceptExpansion(ctx context.Context, request store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	if s.unit == capability.UnitEval {
		if err := s.logs.close(context.WithoutCancel(ctx)); err != nil {
			return store.AcceptedExpansion{}, err
		}
	}
	return s.NativeHostStore.AcceptExpansion(ctx, request)
}

func (s logHostStore) FinishRunAndUnit(ctx context.Context, request store.FinishRunRequest) (store.Applied, error) {
	if err := s.logs.close(context.WithoutCancel(ctx)); err != nil {
		return store.Applied{}, err
	}
	return s.NativeHostStore.FinishRunAndUnit(ctx, request)
}

func (s logHostStore) TransitionNode(ctx context.Context, request store.NodeCommand) (store.Applied, error) {
	switch request.(type) {
	case store.ReportNode, store.RetryNode, store.ReleaseNode:
		ref, _ := store.NodeCommandAuthority(request)
		if err := s.logs.closeNode(context.WithoutCancel(ctx), string(ref.NodeID)); err != nil {
			return store.Applied{}, err
		}
	}
	return s.NativeHostStore.TransitionNode(ctx, request)
}

func (l *localLogs) closeNode(ctx context.Context, node string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result error
	for key, sink := range l.sinks {
		var subject blob.LogSubject
		if err := json.Unmarshal([]byte(key), &subject); err != nil {
			return err
		}
		if subject.Attempt != nil && subject.Attempt.NodeID == node {
			_, err := sink.Close(ctx, logseg.End{})
			delete(l.sinks, key)
			result = errors.Join(result, err)
		}
	}
	return result
}

func (l *localLogs) subject(ctx context.Context, node string) (capability.Principal, blob.LogSubject, error) {
	p, err := l.current(ctx)
	if err != nil {
		return p, blob.LogSubject{}, err
	}
	b := p.Capability.Binding
	subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: p.Capability.Team, RunID: l.run, Unit: &blob.LogUnit{Kind: blob.LogUnitKind(p.Capability.Unit), Generation: b.Generation}}
	if p.Capability.Unit == capability.UnitEval && b.Expansion != nil {
		subject.Unit.Generation = b.Expansion.Generation
		subject.Unit.Source = &blob.LogSource{NodeID: b.NodeID, Generation: b.Generation}
	}
	if node != "" {
		row, err := l.readNode(ctx, controllerwire.NodeRef{RunID: runspec.RunID(l.run), NodeID: runspec.NodeID(node)})
		if err != nil {
			return p, blob.LogSubject{}, err
		}
		if row.Ref != (controllerwire.NodeRef{RunID: runspec.RunID(l.run), NodeID: runspec.NodeID(node)}) {
			return p, blob.LogSubject{}, nodeClaimMismatch()
		}
		if row.Status == store.StatusRunning && row.Attempt > 0 && row.CapabilityPrefix != "" {
			p, err = l.claims.principal(ctx, node, int64(row.Generation), int64(row.Attempt))
			if err != nil {
				return p, blob.LogSubject{}, err
			}
			subject.Kind, subject.Unit = blob.LogJobAttempt, nil
			subject.Attempt = &runspec.AttemptIdentity{Team: p.Capability.Team, RunID: l.run, NodeID: node, Generation: int64(row.Generation), Attempt: int64(row.Attempt)}
		}
	}
	return p, subject, subject.Validate()
}

func (l *localLogs) write(ctx context.Context, run, node string, frame nodeproto.FrameLog) error {
	if run != l.run || l.masker == nil {
		return errors.New("local logs require the awarded run and shared masker")
	}
	p, subject, err := l.subject(ctx, node)
	if err != nil {
		return err
	}
	key, err := json.Marshal(subject)
	if err != nil {
		return err
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return logseg.ErrClosed
	}
	if l.sinks == nil {
		l.sinks = map[string]*logseg.Sink{}
	}
	sink := l.sinks[string(key)]
	if sink == nil {
		uploader, err := l.upload(ctx, p, subject)
		if err != nil {
			return err
		}
		sink, err = logseg.NewSink(ctx, uploader, logseg.Options{Subject: subject})
		if err != nil {
			return err
		}
		l.sinks[string(key)] = sink
	}
	return sink.Append(l.masker.MaskJSON(body), "", 0, 0)
}

func (l *localLogs) close(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	var result error
	for _, sink := range l.sinks {
		_, err := sink.Close(ctx, logseg.End{})
		result = errors.Join(result, err)
	}
	return result
}
