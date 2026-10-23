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
	streams   map[string]*localLogStream
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
	streams := l.take(node)
	done := make(chan struct{})
	l.flushes.Add(1)
	go func() {
		defer l.flushes.Done()
		defer close(done)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limits.LogSealGrace)
		defer cancel()
		for _, stream := range streams {
			if err := stream.close(ctx); err != nil {
				l.alert(node, err)
			}
		}
	}()
	return done
}

func (l *localLogs) take(node string) []*localLogStream {
	l.mu.Lock()
	defer l.mu.Unlock()
	if node == "" {
		l.closed = true
	}
	var streams []*localLogStream
	for key, stream := range l.streams {
		if node == "" || stream.subject.Attempt != nil && stream.subject.Attempt.NodeID == node {
			streams = append(streams, stream)
			delete(l.streams, key)
		}
	}
	return streams
}

type localLogStream struct {
	mu      sync.Mutex
	subject blob.LogSubject
	upload  *logUpload
	sink    *logseg.Sink
	failed  bool
	closed  bool
}

func (s *localLogStream) write(ctx context.Context, body []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return logseg.ErrClosed
	}
	if s.sink == nil {
		var err error
		s.sink, err = logseg.NewSink(ctx, s.upload, logseg.Options{Subject: s.subject})
		if err != nil {
			s.failed = true
			return err
		}
		if s.failed {
			s.sink.MarkIncomplete()
		}
	}
	return s.sink.Append(body, "", 0, 0)
}

func (s *localLogStream) close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.sink == nil {
		manifest := logseg.Manifest{Version: 1, Subject: &s.subject, Status: logseg.StatusComplete, SealedBy: "node", SealedAt: time.Now().UTC()}
		if s.failed {
			manifest.Status, manifest.Reason = "incomplete", "log persistence failed"
		}
		return s.upload.PutManifest(ctx, manifest)
	}
	manifest, err := s.sink.Close(ctx, logseg.End{})
	if err == nil && manifest.Status == "incomplete" {
		err = errors.New(manifest.Reason)
	}
	return err
}

func (l *localLogs) bind(p capability.Principal, attempt int64) {
	node := ""
	if p.Capability.Unit == capability.UnitNode {
		p.Capability.Binding.Attempt = attempt
		node = p.Capability.Binding.NodeID
		l.mu.Lock()
		if l.nodes == nil {
			l.nodes = map[string]capability.Principal{}
		}
		l.nodes[node] = p
		l.mu.Unlock()
	}
	if _, err := l.open(node); err != nil {
		l.alert(node, err)
	}
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
	open func(context.Context) (logseg.Uploader, error)
	up   logseg.Uploader
}

func (u *logUpload) uploader(ctx context.Context) (logseg.Uploader, error) {
	if u.up == nil {
		up, err := u.open(ctx)
		if err != nil {
			return nil, err
		}
		u.up = up
	}
	return u.up, nil
}

func (u *logUpload) PutSegment(ctx context.Context, segment logseg.Segment, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, limits.LogSealGrace)
	defer cancel()
	up, err := u.uploader(ctx)
	if err != nil {
		return err
	}
	return up.PutSegment(ctx, segment, body)
}

func (u *logUpload) PutManifest(ctx context.Context, manifest logseg.Manifest) error {
	up, err := u.uploader(ctx)
	if err != nil {
		return err
	}
	return up.PutManifest(ctx, manifest)
}

func (l *localLogs) open(node string) (*localLogStream, error) {
	p, subject, err := l.subject(node)
	if err != nil {
		return nil, err
	}
	key, err := json.Marshal(subject)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, logseg.ErrClosed
	}
	if l.streams == nil {
		l.streams = map[string]*localLogStream{}
	}
	stream := l.streams[string(key)]
	if stream == nil {
		stream = &localLogStream{subject: subject, upload: &logUpload{open: func(ctx context.Context) (logseg.Uploader, error) { return l.upload(ctx, p, subject) }}}
		l.streams[string(key)] = stream
	}
	return stream, nil
}

func (l *localLogs) write(ctx context.Context, run, node string, frame nodeproto.FrameLog) error {
	if frame.Stream != "" || frame.StreamData != "" || frame.StreamEnd {
		return errors.New("raw SDK streams require the host egress adapter")
	}
	if run != l.run || l.masker == nil {
		return errors.New("local logs require the awarded run and shared masker")
	}
	stream, err := l.open(node)
	if errors.Is(err, logseg.ErrClosed) {
		l.alert(node, err)
		return nil
	}
	if err != nil {
		return err
	}
	body, err := json.Marshal(l.masker.Log(frame))
	if err != nil {
		return err
	}
	if err := stream.write(ctx, body); err != nil {
		l.alert(node, err)
	}
	return nil
}
