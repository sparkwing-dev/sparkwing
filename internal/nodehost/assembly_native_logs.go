package nodehost

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

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
	failed   map[string]capability.Principal
	closed   bool
}

type logHostStore struct {
	NativeHostStore
	logs *localLogs
	unit capability.UnitKind
}

func (s logHostStore) AcceptPlan(ctx context.Context, request store.PlanAcceptance) (store.AcceptedPlan, error) {
	if s.unit == capability.UnitPlan {
		s.logs.finish(context.WithoutCancel(ctx), "")
	}
	return s.NativeHostStore.AcceptPlan(ctx, request)
}

func (s logHostStore) AcceptExpansion(ctx context.Context, request store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	if s.unit == capability.UnitEval {
		s.logs.finish(context.WithoutCancel(ctx), "")
	}
	return s.NativeHostStore.AcceptExpansion(ctx, request)
}

func (s logHostStore) FinishRunAndUnit(ctx context.Context, request store.FinishRunRequest) (store.Applied, error) {
	s.logs.finish(context.WithoutCancel(ctx), "")
	return s.NativeHostStore.FinishRunAndUnit(ctx, request)
}

func (s logHostStore) TransitionNode(ctx context.Context, request store.NodeCommand) (store.Applied, error) {
	switch request.(type) {
	case store.ReportNode, store.RetryNode, store.ReleaseNode:
		ref, _ := store.NodeCommandAuthority(request)
		s.logs.finish(context.WithoutCancel(ctx), string(ref.NodeID))
	}
	return s.NativeHostStore.TransitionNode(ctx, request)
}

func (l *localLogs) alert(node string, err error) {
	slog.Error("run logs incomplete", "run", l.run, "node", node, "error", l.masker.Mask(err.Error()))
}

func (l *localLogs) finish(ctx context.Context, node string) {
	var err error
	if node == "" {
		err = l.close(ctx)
	} else {
		err = l.closeNode(ctx, node)
	}
	if err != nil {
		l.alert(node, err)
	}
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
			manifest, err := sink.Close(ctx, logseg.End{})
			if err == nil && manifest.Status == "incomplete" {
				l.alert(node, errors.New(manifest.Reason))
			}
			delete(l.sinks, key)
			result = errors.Join(result, err)
		}
	}
	return errors.Join(result, l.closeFailed(ctx, node))
}

func (l *localLogs) closeFailed(ctx context.Context, node string) error {
	var result error
	for key, principal := range l.failed {
		var subject blob.LogSubject
		if err := json.Unmarshal([]byte(key), &subject); err != nil {
			result = errors.Join(result, err)
			continue
		}
		if node != "" && (subject.Attempt == nil || subject.Attempt.NodeID != node) {
			continue
		}
		uploader, err := l.upload(ctx, principal, subject)
		if err == nil {
			err = uploader.PutManifest(ctx, logseg.Manifest{Version: 1, Subject: &subject, Status: "incomplete", Reason: "log persistence failed", SealedBy: "node", SealedAt: time.Now().UTC()})
		}
		result = errors.Join(result, err)
		delete(l.failed, key)
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
	if frame.Stream != "" || frame.StreamData != "" || frame.StreamEnd {
		return errors.New("raw SDK streams require the host egress adapter")
	}
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
	body, err := json.Marshal(l.masker.Log(frame))
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		l.alert(node, logseg.ErrClosed)
		return nil
	}
	if l.sinks == nil {
		l.sinks = map[string]*logseg.Sink{}
	}
	sink := l.sinks[string(key)]
	if sink == nil {
		uploader, err := l.upload(ctx, p, subject)
		if err == nil {
			sink, err = logseg.NewSink(ctx, uploader, logseg.Options{Subject: subject})
		}
		if err != nil {
			l.alert(node, err)
			if l.failed == nil {
				l.failed = map[string]capability.Principal{}
			}
			l.failed[string(key)] = p
			return nil
		}
		l.sinks[string(key)] = sink
		// Earlier discarded bytes must remain visible even if storage recovers.
		if _, failed := l.failed[string(key)]; failed {
			sink.MarkIncomplete()
			delete(l.failed, string(key))
		}
	}
	if err := sink.Append(body, "", 0, 0); err != nil {
		l.alert(node, err)
	}
	return nil
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
		manifest, err := sink.Close(ctx, logseg.End{})
		if err == nil && manifest.Status == "incomplete" {
			l.alert("", errors.New(manifest.Reason))
		}
		result = errors.Join(result, err)
	}
	return errors.Join(result, l.closeFailed(ctx, ""))
}
