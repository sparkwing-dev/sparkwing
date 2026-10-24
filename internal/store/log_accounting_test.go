package store

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestLogPersistenceCompressedInventory(t *testing.T) {
	_, tenant, actor, attempt := resultFixture(t)
	raw := bytes.Repeat([]byte("compressed log line\n"), 1000)
	var encoded bytes.Buffer
	w := gzip.NewWriter(&encoded)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	commit := logCommit(actor, attempt)
	sum := sha256.Sum256(encoded.Bytes())
	commit.Segments[0].Size = int64(len(raw))
	commit.Segments[0].StoredBytes = int64(encoded.Len())
	commit.Segments[0].Digest = hex.EncodeToString(sum[:])
	for range 2 {
		if err := tenant.RecordSegments(t.Context(), commit); err != nil {
			t.Fatal(err)
		}
	}
	if retained, err := tenant.StorageRetainedBytes(t.Context()); err != nil || retained != int64(encoded.Len()) {
		t.Fatalf("retained=%d, want compressed bytes=%d (decoded=%d): %v", retained, encoded.Len(), len(raw), err)
	}
	for _, offset := range []int64{0, 1, int64(len(raw) - 1)} {
		view, err := tenant.ReadLogSegments(t.Context(), actor, blob.LogReadRequest{Subject: commit.Subject, Offset: offset})
		if err != nil || !reflect.DeepEqual(view.Segments, commit.Segments) {
			t.Fatalf("decoded cursor %d: %+v: %v", offset, view, err)
		}
	}
	if view, err := tenant.ReadLogSegments(t.Context(), actor, blob.LogReadRequest{Subject: commit.Subject, Offset: int64(len(raw))}); err != nil || len(view.Segments) != 0 {
		t.Fatalf("decoded end cursor: %+v: %v", view, err)
	}
	for _, field := range []string{"decoded", "stored"} {
		bad := commit
		bad.Segments = append([]runstate.Segment(nil), commit.Segments...)
		if field == "decoded" {
			bad.Segments[0].Size++
		} else {
			bad.Segments[0].StoredBytes++
		}
		if err := tenant.RecordSegments(t.Context(), bad); err == nil {
			t.Fatalf("%s byte count changed on replay", field)
		}
	}
	for _, size := range []int64{-1, 0, limits.LogSegmentUploadBytes + 1} {
		bad := commit
		bad.Segments = append([]runstate.Segment(nil), commit.Segments...)
		bad.Segments[0].StoredBytes = size
		if err := tenant.RecordSegments(t.Context(), bad); err == nil {
			t.Fatalf("invalid stored byte count %d accepted", size)
		}
	}
	if retained, err := tenant.StorageRetainedBytes(t.Context()); err != nil || retained != int64(encoded.Len()) {
		t.Fatalf("refused writes changed retained bytes=%d: %v", retained, err)
	}
}
