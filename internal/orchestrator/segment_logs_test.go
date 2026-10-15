package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestNodeLogRequiresAuthenticatedBinding(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if sink := openNodeLogSink(t.Context(), logseg.Data{}, nil, logger); sink != nil {
		t.Fatal("unbound log opened")
	}
}

func TestHeartbeatWatchReportsTurnTheLiveViewOnAndOff(t *testing.T) {
	subject := blob.LogSubject{Kind: blob.LogJobAttempt, Team: "acme", RunID: "run", Attempt: &runspec.AttemptIdentity{Team: "acme", RunID: "run", NodeID: "job", Attempt: 2, Generation: 7}}
	boundary := &sinkLogBoundary{subject: subject}
	pushes := make(chan string, 4)
	ctrl := client.New("https://controller.test", &http.Client{Transport: sinkLiveTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/runs/run/nodes/job/logs" || r.Header.Get(store.LiveLogAttemptHeader) != "a2-g7" || r.Header.Get(store.LiveLogOffsetHeader) != "0" {
			return nil, fmt.Errorf("unexpected live request: %s %v", r.URL.Path, r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		pushes <- string(body)
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sink := openNodeLogSink(t.Context(), logseg.Data{Subject: subject, Transport: boundary, Records: boundary, HTTPClient: boundary}, ctrl, logger)
	if sink == nil {
		t.Fatal("no sink")
	}
	for i, line := range []string{"one\n", "two\n", "three\n"} {
		if err := sink.sink.Append([]byte(line), "", 0, 0); err != nil {
			t.Fatal(err)
		}
		switch i {
		case 0:
			NoteWatch(client.WatchReport{RunID: "run", WholeRun: true, Nodes: []string{"job"}})
		case 1:
			NoteWatch(client.WatchReport{RunID: "run", NodeID: "job", Watched: false})
		}
	}
	sink.close(t.Context(), false)
	close(pushes)
	var got strings.Builder
	for push := range pushes {
		got.WriteString(push)
	}
	if got.String() != "one\ntwo\n" {
		t.Fatalf("live pushes=%q", got.String())
	}
	if len(boundary.commits) != 2 || !boundary.commits[1].Final {
		t.Fatalf("unsealed segments: %+v", boundary.commits)
	}
}

type sinkLiveTransport func(*http.Request) (*http.Response, error)

func (f sinkLiveTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func FuzzNodeLogKeepsBoundSubject(f *testing.F) {
	f.Add(uint8(0), uint16(1), uint16(7))
	f.Add(uint8(1), uint16(2), uint16(9))
	f.Add(uint8(2), uint16(3), uint16(11))
	f.Add(uint8(3), uint16(4), uint16(13))
	f.Fuzz(func(t *testing.T, kind uint8, ordinal, generation uint16) {
		subject := blob.LogSubject{
			Team: "acme", RunID: "run", Kind: blob.LogJobAttempt,
			Attempt: &runspec.AttemptIdentity{Team: "acme", RunID: "run", NodeID: "job", Attempt: int64(ordinal) + 1, Generation: int64(generation) + 1},
		}
		if kind%4 != 0 {
			subject.Kind, subject.Attempt = blob.LogOwnerUnit, nil
			subject.Unit = &blob.LogUnit{Kind: []blob.LogUnitKind{blob.LogTrigger, blob.LogPlan, blob.LogEval}[kind%4-1], Generation: int64(generation) + 1}
			if subject.Unit.Kind == blob.LogEval {
				subject.Unit.Source = &blob.LogSource{NodeID: "source", Generation: int64(ordinal) + 1}
			}
		}
		expected := subject
		if subject.Attempt != nil {
			expected.Attempt = new(*subject.Attempt)
		} else {
			expected.Unit = new(*subject.Unit)
			if subject.Unit.Source != nil {
				expected.Unit.Source = new(*subject.Unit.Source)
			}
		}
		boundary := &sinkLogBoundary{subject: expected}
		data := logseg.Data{Subject: subject, Transport: boundary, Records: boundary, HTTPClient: boundary, Actor: store.Actor{CapabilityPrefix: "bound-writer"}}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		sink := openNodeLogSink(t.Context(), data, nil, logger)
		if sink == nil {
			t.Fatal("no bound sink")
		}
		if subject.Attempt != nil {
			subject.Attempt.NodeID, subject.Attempt.Generation = "substituted", -1
		} else {
			subject.Unit.Generation = -1
			if subject.Unit.Source != nil {
				subject.Unit.Source.NodeID = "substituted"
			}
		}
		if err := sink.sink.Append([]byte("line one\nline two\n"), "", 0, 0); err != nil {
			t.Fatal(err)
		}
		sink.close(t.Context(), false)
		if len(boundary.commits) != 2 || !boundary.commits[1].Final || boundary.commits[1].IncompleteReason != "" || boundary.transfers != 2 {
			t.Fatalf("commits=%+v transfers=%d", boundary.commits, boundary.transfers)
		}
		for _, commit := range boundary.commits {
			if !reflect.DeepEqual(commit.Subject, expected) || commit.Actor != data.Actor || commit.RetentionClass != "r30" || len(commit.Segments) != 1 {
				t.Fatalf("changed log binding: %+v", commit)
			}
			want, err := blob.LogObjectKey(expected, "r30", 1)
			if err != nil || commit.Segments[0].ObjectKey != want {
				t.Fatalf("object key=%s, want %s: %v", commit.Segments[0].ObjectKey, want, err)
			}
		}
	})
}

type sinkLogBoundary struct {
	subject   blob.LogSubject
	commits   []store.SegmentCommit
	transfers int
}

func (b *sinkLogBoundary) SignSegments(_ context.Context, request blob.LogSigningRequest) (blob.LogUploadBatch, error) {
	if !reflect.DeepEqual(request.Subject, b.subject) {
		return blob.LogUploadBatch{}, blob.ErrIntegrity
	}
	expiry := time.Now().Add(time.Hour)
	batch := blob.LogUploadBatch{Subject: request.Subject, RetentionClass: "r30", ExpiresAt: expiry, ManifestURL: "https://logs.test/manifest"}
	for i := 0; i < request.Count; i++ {
		index := request.FirstSegment + int64(i)
		batch.Segments = append(batch.Segments, blob.LogUploadURL{Index: index, URL: fmt.Sprintf("https://logs.test/%d", index), ExpiresAt: expiry})
	}
	return batch, nil
}

func (*sinkLogBoundary) VerifyUpload(context.Context, blob.LogUploadCheck) error {
	return errors.New("unexpected verification")
}

func (*sinkLogBoundary) ReadLog(context.Context, blob.LogReadRequest) (io.ReadCloser, error) {
	return nil, errors.New("unexpected read")
}

func (b *sinkLogBoundary) RecordSegments(_ context.Context, commit store.SegmentCommit) error {
	b.commits = append(b.commits, commit)
	return nil
}

func (b *sinkLogBoundary) Transfer(_ context.Context, _ blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
	b.transfers++
	_, err := io.Copy(io.Discard, body)
	return nil, err
}
