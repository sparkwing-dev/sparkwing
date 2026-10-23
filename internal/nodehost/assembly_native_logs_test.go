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
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
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
			if err == nil || len(logs.streams) != 0 {
				t.Fatal("refused authority reached storage")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		<-logs.finish(t.Context(), "")
		if revoked {
			if len(records.objects) != 0 {
				t.Fatal("refused writer reached storage")
			}
			return
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

func TestLogSpoolFailureRemainsVisibleAfterRecovery(t *testing.T) {
	records := &nativeLogRecords{objects: map[string][]byte{}}
	logs := &localLogs{run: "run", masker: secrets.NewMasker(), principal: capability.Principal{Capability: capability.Capability{Team: "team", Unit: capability.UnitPlan, Binding: capability.Binding{RunID: "run", Generation: 1}}}, upload: func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
		records.view.Subject = subject
		return records, nil
	}}
	spoolDir := t.TempDir()
	t.Setenv("TMPDIR", spoolDir+"/missing")
	if err := logs.write(t.Context(), "run", "", nodeproto.FrameLog{Message: "discarded"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", spoolDir)
	if err := logs.write(t.Context(), "run", "", nodeproto.FrameLog{Message: "retained"}); err != nil {
		t.Fatal(err)
	}
	<-logs.finish(t.Context(), "")
	if records.view.Complete || records.view.IncompleteReason == "" || len(records.objects) != 1 {
		t.Fatalf("spool recovery hid lost bytes: %+v", records.view)
	}
}

func TestLogUploaderInitializationPreservesBufferedBytes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
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
		for _, message := range []string{"first", "second"} {
			if err := logs.write(t.Context(), "run", "", nodeproto.FrameLog{Message: message}); err != nil {
				t.Fatal(err)
			}
		}
		if calls != 0 {
			t.Fatal("opening a spool contacted log storage")
		}
		<-logs.finish(t.Context(), "")
		if calls != 2 || !records.view.Complete || len(records.objects) != 1 {
			t.Fatalf("uploader recovery lost buffered bytes: calls=%d view=%+v", calls, records.view)
		}
	})
}

func TestLogLifecycleDetachesFlush(t *testing.T) {
	for _, operation := range []string{"plan", "expansion", "finish", "release"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "team", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "team", RunID: "run", NodeID: "job", Generation: 1, Attempt: 1}}
				up := &stalledLogUploader{done: make(chan struct{})}
				sink, err := logseg.NewSink(t.Context(), up, logseg.Options{Subject: subject})
				if err != nil {
					t.Fatal(err)
				}
				key, _ := json.Marshal(subject)
				logs := &localLogs{run: "run", masker: secrets.NewMasker(), streams: map[string]*localLogStream{string(key): {subject: subject, sink: sink}}}
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
	<-logs.finish(t.Context(), "")
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
		up := &stalledLogUploader{done: make(chan struct{})}
		logs := &localLogs{run: "run", masker: secrets.NewMasker(), upload: func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
			if subject.Attempt.NodeID == "slow" {
				return up, nil
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
		done := logs.finish(t.Context(), "slow")
		synctest.Wait()
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

func TestStalledLogUploaderInitializationDoesNotDelayAnotherJobsReport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "slow", Steps: []nodeproto.StepSpec{{ID: "body"}}}, {ID: "healthy", Steps: []nodeproto.StepSpec{{ID: "body"}}}}}, teststore.WithClock(func() time.Time { return now }))
		actors := map[string]store.Actor{}
		for _, node := range []string{"slow", "healthy"} {
			ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: runspec.NodeID(node)}
			if _, err := f.Tenant.TransitionNode(t.Context(), store.ReadyNode{Ref: ref, Actor: f.Actor}); err != nil {
				t.Fatal(err)
			}
			award, err := f.Tenant.ReconcileNode(t.Context(), store.ReconcileRequest{Ref: ref, Actor: f.Actor, Policy: store.SourceNone})
			if err != nil || award.Kind != store.Execute {
				t.Fatalf("award: %+v %v", award, err)
			}
			actor := store.Actor{Kind: store.ActorHolder, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: award.Node.CapabilityPrefix}, CapabilityPrefix: award.Node.CapabilityPrefix, NodeFence: runspec.NodeFence{RunID: ref.RunID, NodeID: ref.NodeID, Holder: award.Node.ClaimedBy, Generation: award.Node.Generation}}
			if err := awaitStart(t.Context(), f.Tenant, store.CapacityBinding{RunID: string(ref.RunID), NodeID: node, Generation: int64(actor.Generation)}, actor, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: actor, SourceAttempt: &award.Node.Attempt}); err != nil {
				t.Fatal(err)
			}
			actors[node] = actor
		}
		principal, err := f.Store.AsOperator().ResolveAuthority(t.Context(), actors["slow"].Authority)
		if err != nil {
			t.Fatal(err)
		}
		logs := &localLogs{run: f.Accepted.RunID, masker: secrets.NewMasker(), upload: func(context.Context, capability.Principal, blob.LogSubject) (logseg.Uploader, error) {
			time.Sleep(3 * time.Second)
			return nil, errors.New("storage unavailable")
		}}
		logs.bind(principal, 1)
		wrote := make(chan struct{})
		go func() {
			defer close(wrote)
			if err := logs.write(t.Context(), f.Accepted.RunID, "slow", nodeproto.FrameLog{Message: "work"}); err != nil {
				t.Error(err)
			}
			<-logs.finish(t.Context(), "slow")
		}()
		synctest.Wait()
		state := logHostStore{NativeHostStore: f.Tenant, logs: logs}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		ref := controllerwire.NodeRef{RunID: runspec.RunID(f.Accepted.RunID), NodeID: "healthy"}
		attempt := runspec.AttemptOrdinal(1)
		if _, err := state.TransitionNode(ctx, store.ReportNode{Ref: ref, Actor: actors["healthy"], SourceAttempt: &attempt, Outcome: store.NodeSuccess}); err != nil {
			t.Errorf("another job's stalled uploader blocked terminal commit: %v", err)
		}
		row, err := f.Tenant.ReadNode(t.Context(), ref)
		if err != nil || row.Outcome != store.NodeSuccess || row.Status != store.StatusDone {
			t.Errorf("terminal result was not committed: %+v %v", row, err)
		}
		<-wrote
		<-logs.finish(t.Context(), "")
		logs.flushes.Wait()
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

func TestStalledLogsDoNotDelayLifecycleCommits(t *testing.T) {
	for _, operation := range []string{"report", "retry"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				now := time.Now()
				f := newStoreHostFixture(t, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", Retry: &nodeproto.RetrySpec{Attempts: 3}, Steps: []nodeproto.StepSpec{{ID: "body"}}}}}, teststore.WithClock(func() time.Time { return now }))
				input := storedExecutionInput(t, f, &ClaimedNodeHost{})
				ref := controllerwire.NodeRef{RunID: runspec.RunID(input.Accepted.RunID), NodeID: "job"}
				actor := executorActor(input)
				if err := awaitStart(t.Context(), f.Tenant, store.CapacityBinding{RunID: string(ref.RunID), NodeID: "job", Generation: int64(actor.Generation)}, actor, nil); err != nil {
					t.Fatal(err)
				}
				started, err := f.Tenant.TransitionNode(t.Context(), store.StartNode{Ref: ref, Actor: actor, SourceAttempt: &input.Attempt})
				if err != nil {
					t.Fatal(err)
				}
				principal, err := f.Store.AsOperator().ResolveAuthority(t.Context(), actor.Authority)
				if err != nil {
					t.Fatal(err)
				}
				gate := make(chan struct{})
				registered := blob.LogSubject{Kind: blob.LogJobAttempt, Team: principal.Capability.Team, RunID: input.Accepted.RunID, Attempt: &runspec.AttemptIdentity{Team: principal.Capability.Team, RunID: input.Accepted.RunID, NodeID: "job", Generation: int64(input.Generation), Attempt: int64(started.Attempt)}}
				uploader := &fencedLogUploader{tenant: f.Tenant, commit: store.SegmentCommit{Subject: registered, Actor: actor, RetentionClass: "r30"}, gate: gate}
				logs := &localLogs{run: input.Accepted.RunID, masker: secrets.NewMasker(), upload: func(context.Context, capability.Principal, blob.LogSubject) (logseg.Uploader, error) {
					return uploader, nil
				}}
				defer func() {
					select {
					case <-gate:
					default:
						close(gate)
					}
					<-logs.finish(t.Context(), "")
					logs.flushes.Wait()
				}()
				view, err := f.Tenant.ReadRunView(t.Context(), input.Accepted.RunID)
				found := false
				for _, subject := range view.Logs {
					found = found || subject.Attempt != nil && *subject.Attempt == *registered.Attempt
				}
				if err != nil || !found {
					t.Fatalf("started attempt has no persisted stream before output: %+v %v", view.Logs, err)
				}
				logs.bind(principal, int64(started.Attempt))
				if err := logs.write(t.Context(), input.Accepted.RunID, "job", nodeproto.FrameLog{Message: "attempt output"}); err != nil {
					t.Fatal(err)
				}
				state := logHostStore{NativeHostStore: f.Tenant, logs: logs}
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				var command store.NodeCommand = store.ReportNode{Ref: ref, Actor: actor, SourceAttempt: &started.Attempt, Outcome: store.NodeSuccess, Result: &store.NodeResult{Outcome: store.NodeSuccess}}
				if operation == "retry" {
					command = store.RetryNode{Ref: ref, Actor: actor, SourceAttempt: &started.Attempt, Result: &store.NodeResult{Outcome: store.NodeFailed}}
				}
				if _, err := state.TransitionNode(ctx, command); err != nil {
					t.Fatalf("stalled logs blocked %s: %v", operation, err)
				}
				close(gate) // The flush cannot reach the store until the lifecycle write has committed.
				logs.flushes.Wait()
				row, err := f.Tenant.ReadNode(t.Context(), ref)
				if err != nil || operation == "report" && row.Outcome != store.NodeSuccess || operation == "retry" && row.Attempt != started.Attempt+1 {
					t.Fatalf("lifecycle result changed: %+v %v", row, err)
				}
				if operation == "report" {
					if uploader.refused {
						t.Fatal("current attempt's final tail was refused")
					}
					return
				}
				if !uploader.refused {
					t.Fatal("retry permitted the older attempt's detached flush")
				}
				now = now.Add(limits.LogSealGrace + time.Second)
				op := f.Store.AsOperator()
				lease, err := op.AcquireSingletonLease(t.Context(), "maintenance", "logs", time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if count, err := op.SealAbandonedLogStreams(t.Context(), lease, 10); err != nil || count != 1 {
					t.Fatalf("lost attempt stream was not sealed: %d %v", count, err)
				}
				_, reader, err := teststore.ProvisionCredential(t.Context(), f.Store, capability.ProvisionSpec{Kind: capability.ProvisionMember, Team: string(store.DefaultTeam), Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "log-reader"}, Grants: []capability.Grant{capability.LogsRead}, Binding: capability.Binding{Kind: capability.BindingNone}})
				if err != nil {
					t.Fatal(err)
				}
				stream, err := f.Tenant.ReadLogSegments(t.Context(), store.Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: reader.Prefix}}, blob.LogReadRequest{Subject: registered})
				if err != nil || stream.Complete || stream.IncompleteReason != "writer_lost" || len(stream.Segments) != 0 {
					t.Fatalf("retry hid incomplete logs: %+v %v", stream, err)
				}
			})
		})
	}
}

type fencedLogUploader struct {
	tenant  *store.Tenant
	commit  store.SegmentCommit
	gate    <-chan struct{}
	refused bool
}

func (u *fencedLogUploader) PutSegment(ctx context.Context, segment logseg.Segment, _ []byte) error {
	select {
	case <-u.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	time.Sleep(3 * time.Second)
	key, err := blob.LogObjectKey(u.commit.Subject, u.commit.RetentionClass, int64(segment.N))
	if err != nil {
		return err
	}
	commit := u.commit
	commit.Segments = []store.Segment{{Index: int64(segment.N), Size: segment.Bytes, StoredBytes: segment.StoredBytes, Digest: segment.SHA256, ObjectKey: key}}
	err = u.tenant.RecordSegments(ctx, commit)
	u.refused = err != nil
	return err
}

func (u *fencedLogUploader) PutManifest(ctx context.Context, manifest logseg.Manifest) error {
	commit := u.commit
	commit.Final = true
	if manifest.Status != logseg.StatusComplete {
		commit.IncompleteReason = manifest.Reason
	}
	return u.tenant.RecordSegments(ctx, commit)
}
