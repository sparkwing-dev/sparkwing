package logseg

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
)

type logPutSigner struct {
	keys    []string
	sizes   []int64
	digests []blob.Digest
	expires []time.Time
	err     error
}

func (s *logPutSigner) SignLogPut(_ context.Context, key string, size int64, digest blob.Digest, expiry time.Time) (string, map[string]string, error) {
	s.keys = append(s.keys, key)
	s.sizes = append(s.sizes, size)
	s.digests = append(s.digests, digest)
	s.expires = append(s.expires, expiry)
	return "https://objects.test/" + key, map[string]string{"X-Object-Key": key}, s.err
}

var signingDigest = blob.Digest(strings.Repeat("ab", 32))

func TestLogSigningBindsOneExactObject(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)} {
		for _, class := range []string{"r30", "r90", "r120"} {
			for _, index := range []int64{0, 17} {
				t.Run(fmt.Sprint(subject, class, index), func(t *testing.T) {
					backend := &logPutSigner{}
					var checked []string
					signer := Signer{Objects: backend, Now: func() time.Time { return now }, ClassReady: func(_ context.Context, class string) error {
						checked = append(checked, class)
						return nil
					}}
					req := blob.LogSigningRequest{Subject: subject, Index: index, Size: 42, Digest: signingDigest}
					slot, err := signer.Sign(t.Context(), req, class)
					if err != nil {
						t.Fatal(err)
					}
					prefix, err := blob.LogSubjectPrefix(class, subject)
					if err != nil {
						t.Fatal(err)
					}
					key := blob.LogManifestKey(prefix)
					if index > 0 {
						key = blob.LogSegmentKey(prefix, int(index))
					}
					if !reflect.DeepEqual(slot.Subject, subject) || slot.RetentionClass != class || slot.Index != index || !slot.ExpiresAt.Equal(now.Add(15*time.Minute)) || !reflect.DeepEqual(checked, []string{class}) {
						t.Fatalf("slot = %+v, lifecycle = %v", slot, checked)
					}
					if !reflect.DeepEqual(backend.keys, []string{key}) || backend.sizes[0] != 42 || backend.digests[0] != signingDigest || !backend.expires[0].Equal(slot.ExpiresAt) || slot.URL != "https://objects.test/"+key || slot.Headers["X-Object-Key"] != key {
						t.Fatalf("signed %+v for slot %+v", backend, slot)
					}
					if slot.Subject.Attempt != nil {
						slot.Subject.Attempt.Generation++
					} else {
						slot.Subject.Unit.Generation++
					}
					if req.Subject.Attempt != nil && req.Subject.Attempt.Generation != 7 || req.Subject.Unit != nil && req.Subject.Unit.Generation != 3 {
						t.Fatal("returned slot aliases request identity")
					}
				})
			}
		}
	}
}

func TestLogSigningRejectsBeforeSigning(t *testing.T) {
	for name, change := range map[string]func(*blob.LogSigningRequest, *string){
		"negative index":  func(r *blob.LogSigningRequest, _ *string) { r.Index = -1 },
		"past last":       func(r *blob.LogSigningRequest, _ *string) { r.Index = int64(limits.MaxLogSegmentNumber) + 1 },
		"invalid subject": func(r *blob.LogSigningRequest, _ *string) { r.Subject.Attempt.Generation = 0 },
		"invalid class":   func(_ *blob.LogSigningRequest, c *string) { *c = "r030" },
	} {
		t.Run(name, func(t *testing.T) {
			backend := &logPutSigner{}
			signer := Signer{Objects: backend, ClassReady: func(context.Context, string) error { t.Fatal("checked invalid request"); return nil }}
			req, class := blob.LogSigningRequest{Subject: jobSubject(), Index: 1, Size: 1, Digest: signingDigest}, "r90"
			change(&req, &class)
			if _, err := signer.Sign(t.Context(), req, class); err == nil || len(backend.keys) != 0 {
				t.Fatalf("sign = %v, keys = %v", err, backend.keys)
			}
		})
	}
}

func TestLogSigningRequiresLifecycleAndPreservesErrors(t *testing.T) {
	request := blob.LogSigningRequest{Subject: jobSubject(), Index: 1, Size: 1, Digest: signingDigest}
	backend := &logPutSigner{}
	signer := Signer{Objects: backend}
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
	if slot, err := signer.Sign(t.Context(), request, "r90"); !errors.Is(err, backend.err) || slot.URL != "" {
		t.Fatalf("failed slot = %+v, %v", slot, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	n := len(backend.keys)
	if _, err := signer.Sign(ctx, request, "r90"); !errors.Is(err, context.Canceled) || len(backend.keys) != n {
		t.Fatalf("cancel = %v, keys = %v", err, backend.keys)
	}
}
