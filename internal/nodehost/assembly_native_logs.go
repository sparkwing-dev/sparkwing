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
	"github.com/sparkwing-dev/sparkwing/internal/limits"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type localLogs struct {
	mu        sync.Mutex
	flushes   sync.WaitGroup
	run       string
	masker    *secrets.Masker
	principal capability.Principal
	nodes     map[string]capability.Principal
	upload    func(context.Context, capability.Principal, blob.LogSubject) (logseg.Uploader, error)
	sinks     map[string]*logseg.Sink
	failed    map[string]capability.Principal
	closed    bool
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

// finish detaches streams before committing lifecycle state. Uploads share no host lock.
func (l *localLogs) finish(ctx context.Context, node string) <-chan struct{} {
	sinks, failed := l.take(node)
	done := make(chan struct{})
	l.flushes.Add(1)
	go func() {
		defer l.flushes.Done()
		defer close(done)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limits.LogSealGrace)
		defer cancel()
		if err := l.flush(ctx, sinks, failed); err != nil {
			l.alert(node, err)
		}
	}()
	return done
}

func (l *localLogs) take(node string) (map[string]*logseg.Sink, map[string]capability.Principal) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sinks := map[string]*logseg.Sink{}
	failed := map[string]capability.Principal{}
	if node == "" {
		l.closed = true
	}
	matches := func(key string) bool {
		var subject blob.LogSubject
		return node == "" || json.Unmarshal([]byte(key), &subject) == nil && subject.Attempt != nil && subject.Attempt.NodeID == node
	}
	for key, sink := range l.sinks {
		if matches(key) {
			sinks[key] = sink
			delete(l.sinks, key)
		}
	}
	for key, principal := range l.failed {
		if matches(key) {
			failed[key] = principal
			delete(l.failed, key)
		}
	}
	return sinks, failed
}

func (l *localLogs) flush(ctx context.Context, sinks map[string]*logseg.Sink, failed map[string]capability.Principal) error {
	var result error
	for _, sink := range sinks {
		manifest, err := sink.Close(ctx, logseg.End{})
		if err == nil && manifest.Status == "incomplete" {
			l.alert("", errors.New(manifest.Reason))
		}
		result = errors.Join(result, err)
	}
	for key, principal := range failed {
		var subject blob.LogSubject
		if err := json.Unmarshal([]byte(key), &subject); err != nil {
			result = errors.Join(result, err)
			continue
		}
		uploader, err := l.upload(ctx, principal, subject)
		if err == nil {
			err = uploader.PutManifest(ctx, logseg.Manifest{Version: 1, Subject: &subject, Status: "incomplete", Reason: "log persistence failed", SealedBy: "node", SealedAt: time.Now().UTC()})
		}
		result = errors.Join(result, err)
	}
	return result
}

func (l *localLogs) bind(p capability.Principal, attempt int64) {
	p.Capability.Binding.Attempt = attempt
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.nodes == nil {
		l.nodes = map[string]capability.Principal{}
	}
	l.nodes[p.Capability.Binding.NodeID] = p
}

// subject uses authority captured at assembly or after a committed attempt's storage binding.
// Persistence still checks the current writer, credential ancestry and attempt fence.
func (l *localLogs) subject(node string) (capability.Principal, blob.LogSubject, error) {
	l.mu.Lock()
	p := l.principal
	if bound, ok := l.nodes[node]; node != "" && ok {
		p = bound
	}
	l.mu.Unlock()
	b := p.Capability.Binding
	subject := blob.LogSubject{Kind: blob.LogOwnerUnit, Team: p.Capability.Team, RunID: l.run, Unit: &blob.LogUnit{Kind: blob.LogUnitKind(p.Capability.Unit), Generation: b.Generation}}
	if p.Capability.Unit == capability.UnitEval && b.Expansion != nil {
		subject.Unit.Generation = b.Expansion.Generation
		subject.Unit.Source = &blob.LogSource{NodeID: b.NodeID, Generation: b.Generation}
	}
	if p.Capability.Unit == capability.UnitNode {
		if b.NodeID != node || b.RunID != l.run || b.Attempt <= 0 {
			return p, blob.LogSubject{}, nodeClaimMismatch()
		}
		subject.Kind, subject.Unit = blob.LogJobAttempt, nil
		subject.Attempt = &runspec.AttemptIdentity{Team: p.Capability.Team, RunID: l.run, NodeID: node, Generation: b.Generation, Attempt: b.Attempt}
	}
	return p, subject, subject.Validate()
}

// logUpload bounds background segment uploads independently of lifecycle calls.
type logUpload struct {
	up logseg.Uploader
}

func (u *logUpload) PutSegment(ctx context.Context, segment logseg.Segment, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, limits.LogSealGrace)
	defer cancel()
	return u.up.PutSegment(ctx, segment, body)
}

func (u *logUpload) PutManifest(ctx context.Context, manifest logseg.Manifest) error {
	return u.up.PutManifest(ctx, manifest)
}

func (l *localLogs) write(ctx context.Context, run, node string, frame nodeproto.FrameLog) error {
	if frame.Stream != "" || frame.StreamData != "" || frame.StreamEnd {
		return errors.New("raw SDK streams require the host egress adapter")
	}
	if run != l.run || l.masker == nil {
		return errors.New("local logs require the awarded run and shared masker")
	}
	p, subject, err := l.subject(node)
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
		// Register while this attempt is current so a refused late flush stays visible.
		uploader, err := l.upload(ctx, p, subject)
		if err == nil {
			sink, err = logseg.NewSink(ctx, &logUpload{up: uploader}, logseg.Options{Subject: subject})
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
