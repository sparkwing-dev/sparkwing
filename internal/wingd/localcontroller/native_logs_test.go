package localcontroller

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

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type completingUnitStore struct {
	nodehost.NativeHostStore
	commit func() error
}

func (s completingUnitStore) AcceptPlan(context.Context, store.PlanAcceptance) (store.AcceptedPlan, error) {
	return store.AcceptedPlan{}, s.commit()
}

func (s completingUnitStore) AcceptExpansion(context.Context, store.ExpansionSubmission) (store.AcceptedExpansion, error) {
	return store.AcceptedExpansion{}, s.commit()
}

func TestCloudUnitPublishesLogsBeforeRetiringItsLease(t *testing.T) {
	for _, kind := range []capability.UnitKind{capability.UnitPlan, capability.UnitEval} {
		t.Run(string(kind), func(t *testing.T) {
			records := &nativeLogRecords{objects: map[string][]byte{}}
			logs := &localLogs{run: "run", masker: secrets.NewMasker(), current: func(context.Context) (capability.Principal, error) {
				b := capability.Binding{RunID: "run", Generation: 1}
				if kind == capability.UnitEval {
					b.NodeID = "source"
					b.Expansion = &capability.LeaseRef{Kind: kind, ID: "unit", Holder: "eval", Generation: 7}
				}
				return capability.Principal{Capability: capability.Capability{Team: "team", Unit: kind, Binding: b}}, nil
			}, upload: func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
				records.view.Subject = subject
				return records, nil
			}}
			if err := logs.write(t.Context(), "run", "", nodeproto.FrameLog{Message: "planning"}); err != nil {
				t.Fatal(err)
			}
			state := logHostStore{NativeHostStore: completingUnitStore{commit: func() error {
				if !records.view.Complete {
					return errors.New("unit lease retired before log publication")
				}
				return nil
			}}, logs: logs, unit: kind}
			var err error
			if kind == capability.UnitPlan {
				_, err = state.AcceptPlan(t.Context(), store.PlanAcceptance{})
			} else {
				_, err = state.AcceptExpansion(t.Context(), store.ExpansionSubmission{})
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
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
		logs := &localLogs{run: "run", masker: masker, current: func(context.Context) (capability.Principal, error) {
			if revoked {
				return capability.Principal{}, errors.New("owner lost")
			}
			return capability.Principal{Capability: capability.Capability{Team: "team", Unit: capability.UnitTrigger, Binding: capability.Binding{RunID: "run", Generation: 1}}}, nil
		}, upload: func(_ context.Context, _ capability.Principal, subject blob.LogSubject) (logseg.Uploader, error) {
			records.view.Subject = subject
			return records, nil
		}}
		run := "run"
		if foreign {
			run = "other"
		}
		attrs, _ := json.Marshal(map[string]string{secret: secret})
		err := logs.write(t.Context(), run, "", nodeproto.FrameLog{Message: secret, Attrs: attrs})
		if foreign || revoked {
			if err == nil || len(logs.sinks) != 0 {
				t.Fatal("refused authority reached storage")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := logs.close(t.Context()); err != nil {
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
