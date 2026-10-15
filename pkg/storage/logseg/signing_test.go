package logseg_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

type logPutSigner struct {
	keys    []string
	expires []time.Time
	err     error
}

func (s *logPutSigner) SignLogPut(_ context.Context, key string, expiry time.Time) (string, map[string]string, error) {
	s.keys = append(s.keys, key)
	s.expires = append(s.expires, expiry)
	return "https://objects.test/" + key, map[string]string{"X-Object-Key": key}, s.err
}

func TestLogBatchSigning(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)} {
		for _, class := range []string{"r30", "r90", "r120"} {
			t.Run(fmt.Sprint(subject, class), func(t *testing.T) {
				backend := &logPutSigner{}
				var checked []string
				signer := logseg.BatchSigner{Objects: backend, Now: func() time.Time { return now }, ClassReady: func(_ context.Context, class string) error {
					checked = append(checked, class)
					return nil
				}}
				req := blob.LogSigningRequest{Subject: subject, FirstSegment: 17, Count: 16}
				batch, err := signer.Sign(t.Context(), req, class)
				if err != nil {
					t.Fatal(err)
				}
				if len(batch.Segments) != 16 || !reflect.DeepEqual(batch.Subject, subject) || batch.RetentionClass != class || !batch.ExpiresAt.Equal(now.Add(15*time.Minute)) || !reflect.DeepEqual(checked, []string{class}) {
					t.Fatalf("batch = %+v, lifecycle = %v", batch, checked)
				}
				prefix, err := blob.LogSubjectPrefix(class, subject)
				if err != nil {
					t.Fatal(err)
				}
				for i, slot := range batch.Segments {
					key := blob.LogSegmentKey(prefix, 17+i)
					if slot.Index != int64(17+i) || slot.URL != "https://objects.test/"+key || slot.Headers["X-Object-Key"] != key || !slot.ExpiresAt.Equal(batch.ExpiresAt) || backend.keys[i] != key || !backend.expires[i].Equal(batch.ExpiresAt) {
						t.Fatalf("slot %d = %+v", i, slot)
					}
				}
				if len(backend.keys) != 17 || backend.keys[16] != blob.LogManifestKey(prefix) || batch.ManifestURL != "https://objects.test/"+backend.keys[16] || batch.ManifestHeaders["X-Object-Key"] != backend.keys[16] {
					t.Fatalf("manifest = %q, keys = %v", batch.ManifestURL, backend.keys)
				}
				if batch.Subject.Attempt != nil {
					batch.Subject.Attempt.Generation++
				} else {
					batch.Subject.Unit.Generation++
				}
				if req.Subject.Attempt != nil && req.Subject.Attempt.Generation != 7 || req.Subject.Unit != nil && req.Subject.Unit.Generation != 3 {
					t.Fatal("returned batch aliases request identity")
				}
			})
		}
	}
}

func TestLogBatchRejectsBeforeSigning(t *testing.T) {
	for name, change := range map[string]func(*blob.LogSigningRequest, *string){
		"zero first":      func(r *blob.LogSigningRequest, _ *string) { r.FirstSegment = 0 },
		"past last":       func(r *blob.LogSigningRequest, _ *string) { r.FirstSegment = int64(limits.MaxLogSegmentNumber) + 1 },
		"range overflow":  func(r *blob.LogSigningRequest, _ *string) { r.FirstSegment = int64(limits.MaxLogSegmentNumber) },
		"zero count":      func(r *blob.LogSigningRequest, _ *string) { r.Count = 0 },
		"negative count":  func(r *blob.LogSigningRequest, _ *string) { r.Count = -1 },
		"too many":        func(r *blob.LogSigningRequest, _ *string) { r.Count = 17 },
		"invalid subject": func(r *blob.LogSigningRequest, _ *string) { r.Subject.Attempt.Generation = 0 },
		"invalid class":   func(_ *blob.LogSigningRequest, c *string) { *c = "r030" },
	} {
		t.Run(name, func(t *testing.T) {
			backend := &logPutSigner{}
			signer := logseg.BatchSigner{Objects: backend, ClassReady: func(context.Context, string) error { t.Fatal("checked invalid request"); return nil }}
			req, class := blob.LogSigningRequest{Subject: jobSubject(), FirstSegment: 1, Count: 16}, "r90"
			change(&req, &class)
			if _, err := signer.Sign(t.Context(), req, class); err == nil || len(backend.keys) != 0 {
				t.Fatalf("sign = %v, keys = %v", err, backend.keys)
			}
		})
	}
}

func TestLogBatchRequiresLifecycleAndPreservesErrors(t *testing.T) {
	request := blob.LogSigningRequest{Subject: jobSubject(), FirstSegment: 1, Count: 16}
	backend := &logPutSigner{}
	signer := logseg.BatchSigner{Objects: backend}
	if _, err := signer.Sign(t.Context(), request, "r90"); err == nil {
		t.Fatal("signed without lifecycle check")
	}
	want := errors.New("lifecycle rule missing")
	signer.ClassReady = func(context.Context, string) error { return want }
	if _, err := signer.Sign(t.Context(), request, "r90"); !errors.Is(err, want) || len(backend.keys) != 0 {
		t.Fatalf("missing lifecycle = %v, keys = %v", err, backend.keys)
	}
	signer.ClassReady = func(context.Context, string) error { return nil }
	backend.err = errors.New("backend unavailable")
	if b, err := signer.Sign(t.Context(), request, "r90"); !errors.Is(err, backend.err) || len(b.Segments) != 0 {
		t.Fatalf("partial batch = %+v, %v", b, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	n := len(backend.keys)
	if _, err := signer.Sign(ctx, request, "r90"); !errors.Is(err, context.Canceled) || len(backend.keys) != n {
		t.Fatalf("cancel = %v, keys = %v", err, backend.keys)
	}
}
