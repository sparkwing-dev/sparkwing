package logseg_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/storage/logseg"
)

type replayObject struct {
	body *observedLogBody
	key  string
	err  error
}

func (o *replayObject) Get(_ context.Context, key string) (io.ReadCloser, error) {
	o.key = key
	if o.body == nil {
		return nil, o.err
	}
	return o.body, o.err
}

func TestLogUploadReplayVerifiesExactObject(t *testing.T) {
	for _, subject := range []blob.LogSubject{jobSubject(), ownerSubject(blob.LogTrigger), ownerSubject(blob.LogPlan), ownerSubject(blob.LogEval)} {
		for _, index := range []int64{0, 1, int64(limits.MaxLogSegmentNumber)} {
			raw := []byte("immutable payload")
			sum := sha256.Sum256(raw)
			check := blob.LogUploadCheck{Subject: subject, Index: index, Size: int64(len(raw)), Digest: blob.Digest(hex.EncodeToString(sum[:]))}
			objects := &replayObject{body: &observedLogBody{Reader: bytes.NewReader(raw)}}
			verifier := logseg.UploadVerifier{Objects: objects}
			if err := verifier.Verify(t.Context(), check, "r90"); err != nil {
				t.Fatal(err)
			}
			prefix, err := blob.LogSubjectPrefix("r90", subject)
			if err != nil {
				t.Fatal(err)
			}
			want := blob.LogManifestKey(prefix)
			if index > 0 {
				want = blob.LogSegmentKey(prefix, int(index))
			}
			if objects.key != want || !objects.body.closed {
				t.Fatalf("verification key = %q, want %q, closed = %v", objects.key, want, objects.body.closed)
			}
		}
	}
}

func TestLogUploadReplayRefusals(t *testing.T) {
	for _, fault := range []string{"subject", "class", "index", "digest", "uppercase", "zero", "negative", "segment_limit", "manifest_limit", "missing", "nil_body", "read_error", "corrupt", "short", "long", "close", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			raw := []byte("immutable payload")
			sum := sha256.Sum256(raw)
			check := blob.LogUploadCheck{Subject: jobSubject(), Index: 1, Size: int64(len(raw)), Digest: blob.Digest(hex.EncodeToString(sum[:]))}
			objects := &replayObject{body: &observedLogBody{Reader: bytes.NewReader(raw)}}
			class := "r90"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch fault {
			case "subject":
				check.Subject.RunID = "other"
			case "class":
				class = "r090"
			case "index":
				check.Index = int64(limits.MaxLogSegmentNumber) + 1
			case "digest":
				check.Digest = "invalid"
			case "uppercase":
				check.Digest = blob.Digest(strings.ToUpper(string(check.Digest)))
			case "zero":
				check.Size = 0
			case "negative":
				check.Index = -1
			case "segment_limit":
				check.Size = limits.LogSegmentUploadBytes + 1
			case "manifest_limit":
				check.Index, check.Size = 0, limits.LogManifestBytes+1
			case "missing":
				objects.err = blob.ErrNotFound
				objects.body = nil
			case "nil_body":
				objects.body = nil
			case "read_error":
				objects.err = errors.New("object read refused")
			case "corrupt":
				objects.body.Reader = bytes.NewReader(bytes.Repeat([]byte{'x'}, len(raw)))
			case "short":
				objects.body.Reader = bytes.NewReader(raw[:len(raw)-1])
			case "long":
				objects.body.Reader = bytes.NewReader(append(raw, 'x'))
			case "close":
				objects.body.err = errors.New("close failed")
			case "cancelled":
				cancel()
			}
			err := (logseg.UploadVerifier{Objects: objects}).Verify(ctx, check, class)
			if err == nil {
				t.Fatal("invalid replay succeeded")
			}
			if fault == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation = %v", err)
			}
			if objects.key != "" && objects.body != nil && !objects.body.closed {
				t.Fatal("replay leaked an object stream")
			}
		})
	}
}
