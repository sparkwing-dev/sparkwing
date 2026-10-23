package nodehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type completingUnitStore struct {
	NativeHostStore
	commit func(context.Context) error
}

func (s completingUnitStore) AcceptPlan(ctx context.Context, _ store.PlanAcceptance) (store.AcceptedPlan, error) {
	return store.AcceptedPlan{}, s.commit(ctx)
}

func (s completingUnitStore) AcceptExpansion(ctx context.Context, _ store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	return store.AcceptedExpansion{}, s.commit(ctx)
}

func (s completingUnitStore) FinishRunAndUnit(ctx context.Context, _ store.FinishRunRequest) (store.Applied, error) {
	return store.Applied{}, s.commit(ctx)
}

func (s completingUnitStore) TransitionNode(ctx context.Context, _ store.NodeCommand) (store.Applied, error) {
	return store.Applied{}, s.commit(ctx)
}

type nativeLogRecords struct {
	view    store.LogSegments
	objects map[string][]byte
}

func (r *nativeLogRecords) PutSegment(_ context.Context, segment logseg.Segment, body []byte) error {
	key, err := blob.LogObjectKey(r.view.Subject, logseg.ClassStandard, int64(segment.N))
	if err != nil {
		return err
	}
	var offset int64
	for _, row := range r.view.Segments {
		offset += row.Size
	}
	r.view.Segments = append(r.view.Segments, store.Segment{Index: int64(segment.N), Offset: offset, Size: segment.Bytes, StoredBytes: segment.StoredBytes, Digest: segment.SHA256, ObjectKey: key})
	r.objects[key] = bytes.Clone(body)
	return nil
}

func (r *nativeLogRecords) PutManifest(_ context.Context, manifest logseg.Manifest) error {
	r.view.Complete = manifest.Status == logseg.StatusComplete
	r.view.IncompleteReason = manifest.Reason
	return nil
}

func (r *nativeLogRecords) ReadLogSegments(context.Context, store.Actor, blob.LogReadRequest) (store.LogSegments, error) {
	return r.view, nil
}

func (r *nativeLogRecords) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(r.objects[key])), nil
}

func FuzzLocalNativeLogsPreserveAuthorityAndMasking(f *testing.F) {
	f.Add("private", false, false)
	f.Add("other", true, false)
	f.Add("revoked", false, true)
	f.Fuzz(func(t *testing.T, value string, foreign, revoked bool) {
		sum := sha256.Sum256([]byte(value))
		secret := hex.EncodeToString(sum[:])
		masker := secrets.NewMasker()
		masker.Register(secret)
		records := &nativeLogRecords{objects: map[string][]byte{}}
		logs := &localLogs{run: "run", masker: masker, principal: capability.Principal{Capability: capability.Capability{Team: "team", Unit: capability.UnitTrigger, Binding: capability.Binding{RunID: "run", Generation: 1}}}, upload: func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
			if revoked {
				return nil, nodeClaimMismatch()
			}
			records.view.Subject = subject
			return records, nil
		}}
		run := "run"
		if foreign {
			run = "other"
		}
		attrs, _ := json.Marshal(map[string]string{secret: secret})
		err := logs.write(t.Context(), run, "", nodeproto.FrameLog{Message: secret, Attrs: attrs})
		if foreign {
			if err == nil || len(logs.sinks) != 0 {
				t.Fatal("refused authority reached storage")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		err = logs.close(t.Context())
		if revoked {
			if err == nil || len(records.objects) != 0 {
				t.Fatal("refused writer reached storage")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		read, err := (logseg.CommittedReader{Records: records, Objects: records}).Read(t.Context(), store.Actor{}, blob.LogReadRequest{Subject: records.view.Subject}, &body)
		if err != nil || !read.Complete || bytes.Contains(body.Bytes(), []byte(secret)) || !json.Valid(bytes.TrimSpace(body.Bytes())) {
			t.Fatalf("shared read roundtrip exposed private or invalid output: %s %v", body.Bytes(), err)
		}
	})
}

func FuzzNativeServicesRejectMissingBindings(f *testing.F) {
	for i := uint8(0); i < 3; i++ {
		f.Add(i)
	}
	f.Fuzz(func(t *testing.T, missing uint8) {
		cfg, _ := nativeConfigFixture(t)
		switch missing % 3 {
		case 0:
			cfg.Services = nil
		case 1:
			cfg.Storage = nil
		case 2:
			cfg.Logs = nil
		}
		opened := false
		_, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { opened = true; return nil, errors.New("opened") })
		if err == nil || opened {
			t.Fatal("incomplete bindings reached process transport")
		}
	})
}

func TestLogSetupFailureRetriesWithoutDiscardingBytes(t *testing.T) {
	records := &nativeLogRecords{objects: map[string][]byte{}}
	calls := 0
	logs := &localLogs{run: "run", masker: secrets.NewMasker(), principal: capability.Principal{Capability: capability.Capability{Team: "team", Unit: capability.UnitPlan, Binding: capability.Binding{RunID: "run", Generation: 1}}}, upload: func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("storage unavailable")
		}
		records.view.Subject = subject
		return records, nil
	}}
	if err := logs.write(t.Context(), "run", "", nodeproto.FrameLog{Message: "retained"}); err != nil {
		t.Fatal(err)
	}
	if err := logs.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !records.view.Complete || len(records.objects) != 1 {
		t.Fatalf("setup retry discarded logs: calls=%d view=%+v", calls, records.view)
	}
}

func TestStalledLogsDoNotDelayLifecycleCommits(t *testing.T) {
	for _, operation := range []string{"plan", "expansion", "finish", "report", "retry", "release"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "team", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Generation: 1, Attempt: 1}}
				up := &stalledLogUploader{done: make(chan struct{})}
				sink, err := logseg.NewSink(t.Context(), up, logseg.Options{Subject: subject})
				if err != nil {
					t.Fatal(err)
				}
				key, _ := json.Marshal(subject)
				logs := &localLogs{run: "run", masker: secrets.NewMasker(), sinks: map[string]*logseg.Sink{string(key): sink}}
				t.Cleanup(logs.flushes.Wait)
				committed := false
				state := logHostStore{NativeHostStore: completingUnitStore{commit: func(ctx context.Context) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					committed = true
					return nil
				}}, logs: logs}
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				ref := controllerwire.NodeRef{RunID: "run", NodeID: "job"}
				switch operation {
				case "plan":
					state.unit = capability.UnitPlan
					_, err = state.AcceptPlan(ctx, store.PlanAcceptance{})
				case "expansion":
					state.unit = capability.UnitEval
					_, err = state.AcceptExpansion(ctx, store.ExpansionSubmission{})
				case "finish":
					_, err = state.FinishRunAndUnit(ctx, store.FinishRunRequest{})
				case "report":
					_, err = state.TransitionNode(ctx, store.ReportNode{Ref: ref})
				case "retry":
					_, err = state.TransitionNode(ctx, store.RetryNode{Ref: ref})
				case "release":
					_, err = state.TransitionNode(ctx, store.ReleaseNode{Ref: ref})
				}
				if err != nil || !committed {
					t.Errorf("stalled logs blocked %s: %v", operation, err)
				}
				<-up.done
				synctest.Wait()
			})
		})
	}
}

type stalledLogUploader struct{ done chan struct{} }

func (*stalledLogUploader) PutSegment(context.Context, logseg.Segment, []byte) error { return nil }
func (u *stalledLogUploader) PutManifest(ctx context.Context, _ logseg.Manifest) error {
	defer close(u.done)
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestNativeLogsUseCommittedAttemptWithoutMetadataReads(t *testing.T) {
	cfg, state := nativeConfigFixture(t)
	refreshFailed := false
	refresh := cfg.Refresh
	cfg.Refresh = func(ctx context.Context) (capability.Principal, error) {
		if refreshFailed {
			return capability.Principal{}, errors.New("principal refresh unavailable")
		}
		return refresh(ctx)
	}
	reads := &unavailableLogMetadata{NativeHostStore: state}
	cfg.Store = reads
	records := &nativeLogRecords{objects: map[string][]byte{}}
	cfg.Logs = func(_ context.Context, p capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
		if p.Capability.Binding.Attempt != 3 {
			t.Errorf("log authority lost committed attempt: %+v", p.Capability.Binding)
		}
		records.view.Subject = subject
		return records, nil
	}
	listener := &processListener{closed: make(chan struct{})}
	services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return listener, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = services.Close() })
	input := NodeInput{NodeID: "job", Generation: 2, Attempt: 3}
	if _, err := services.Config.Executor.BindStorage(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	refreshFailed = true
	logs := services.Config.Store.(logHostStore).logs
	if err := logs.write(t.Context(), "run", "job", nodeproto.FrameLog{Message: "successful work"}); err != nil {
		t.Fatalf("logging-only metadata failure escaped: %v", err)
	}
	if err := logs.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := runspec.AttemptIdentity{Team: "default", RunID: "run", NodeID: "job", Generation: 2, Attempt: 3}
	if records.view.Subject.Attempt == nil || *records.view.Subject.Attempt != want || !records.view.Complete || reads.calls != 0 {
		t.Fatalf("logs lost committed attempt or reread metadata: %+v reads=%d", records.view, reads.calls)
	}
}

type unavailableLogMetadata struct {
	NativeHostStore
	calls int
}

func (s *unavailableLogMetadata) ReadNode(context.Context, controllerwire.NodeRef) (store.NodeRow, error) {
	s.calls++
	return store.NodeRow{}, errors.New("node lookup unavailable")
}

func TestLogUploadsDoNotHoldOtherJobs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		logs := &localLogs{run: "run", masker: secrets.NewMasker(), upload: func(ctx context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
			if subject.Attempt.NodeID == "slow" {
				close(started)
				timer := time.NewTimer(3 * time.Second)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return &nativeLogRecords{view: store.LogSegments{Subject: subject}, objects: map[string][]byte{}}, nil
		}}
		for _, node := range []string{"slow", "healthy"} {
			logs.bind(capability.Principal{Capability: capability.Capability{Team: "team", Unit: capability.UnitNode, Binding: capability.Binding{RunID: "run", NodeID: node, Generation: 1}}}, 1)
		}
		before := time.Now()
		if err := logs.write(t.Context(), "run", "slow", nodeproto.FrameLog{Message: "work"}); err != nil {
			t.Fatal(err)
		}
		if time.Now() != before {
			t.Fatal("stream registration delayed log delivery")
		}
		done := logs.finish(t.Context(), "slow")
		<-started
		if err := logs.write(t.Context(), "run", "healthy", nodeproto.FrameLog{Message: "work"}); err != nil {
			t.Fatal(err)
		}
		if time.Now() != before {
			t.Fatal("another job waited on log storage")
		}
		<-logs.finish(t.Context(), "healthy")
		<-done
	})
}

func TestNativeServiceCloseJoinsLogsAfterTerminalCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg, _ := nativeConfigFixture(t)
		committed := false
		cfg.Store = completingUnitStore{NativeHostStore: cfg.Store, commit: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			committed = true
			return nil
		}}
		up := &stalledLogUploader{done: make(chan struct{})}
		cfg.Logs = func(context.Context, capability.Principal, blob.LogSubject) (logseg.Uploader, error) { return up, nil }
		listener := &processListener{closed: make(chan struct{})}
		services, err := nativeServices(t.Context(), cfg, func(string, string) (net.Listener, error) { return listener, nil })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := services.Config.Executor.BindStorage(t.Context(), NodeInput{NodeID: "job", Generation: 2, Attempt: 1}); err != nil {
			t.Fatal(err)
		}
		logs := services.Config.Store.(logHostStore).logs
		if err := logs.write(t.Context(), "run", "job", nodeproto.FrameLog{Message: "work"}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		if _, err := services.Config.Store.TransitionNode(ctx, store.ReportNode{Ref: controllerwire.NodeRef{RunID: "run", NodeID: "job"}}); err != nil || !committed {
			t.Fatalf("terminal commit depended on logs: %v", err)
		}
		if err := services.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-up.done:
		default:
			t.Fatal("transport retired before detached log flush finished")
		}
	})
}

func TestLogShutdownEffortEndsAtTailBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		up := &cancelledLogUploader{}
		logs := &localLogs{run: "run", masker: secrets.NewMasker(), principal: capability.Principal{Capability: capability.Capability{Team: "team", Unit: capability.UnitPlan, Binding: capability.Binding{RunID: "run", Generation: 1}}}, upload: func(context.Context, capability.Principal, blob.LogSubject) (logseg.Uploader, error) { return up, nil }}
		if err := logs.write(t.Context(), "run", "", nodeproto.FrameLog{Message: "buffered work"}); err != nil {
			t.Fatal(err)
		}
		before := time.Now()
		<-logs.finish(t.Context(), "")
		logs.flushes.Wait()
		if elapsed := time.Since(before); elapsed != limits.LogSealGrace || up.manifest.Status != "incomplete" {
			t.Fatalf("shutdown budget=%v manifest=%+v", elapsed, up.manifest)
		}
	})
}

type cancelledLogUploader struct{ manifest logseg.Manifest }

func (*cancelledLogUploader) PutSegment(ctx context.Context, _ logseg.Segment, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}

func (u *cancelledLogUploader) PutManifest(ctx context.Context, m logseg.Manifest) error {
	u.manifest = m
	return ctx.Err()
}
