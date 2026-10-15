package acceptance_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
)

func checkStorageFilesystemLogRecords(t *testing.T) {
	storageProperty(t, func(seed [16]byte, ordinal uint8, lost bool) bool {
		objects, _ := storageFilesystem(t)
		signing := &storageLogTransport{objects: objects, class: logseg.ClassFree}
		var transport http.RoundTripper = storageFilesystemHTTP(objects)
		if lost {
			transport = storageLostResponse{storageFilesystemHTTP(objects)}
		}
		if err := checkBlobLogRecords(t.Context(), t.TempDir(), signing, objects, blob.TransferClient{Transport: transport}, seed, 1+int(ordinal%17)); err != nil {
			t.Fatal(err)
		}
		return true
	})
}

type storageLogObjects interface {
	logseg.LogPutSigner
	logseg.SegmentObjects
}

type storageLogTransport struct {
	blob.LogTransport
	objects storageLogObjects
	class   string
	signs   int
	checks  int
}

func (s *storageLogTransport) SignSegments(ctx context.Context, q blob.LogSigningRequest) (blob.LogUploadBatch, error) {
	s.signs++
	ready := func(context.Context, string) error { return nil }
	if objects, ok := s.objects.(interface {
		LogClassReady(context.Context, string) error
	}); ok {
		ready = objects.LogClassReady
	}
	return (logseg.BatchSigner{Objects: s.objects, ClassReady: ready}).Sign(ctx, q, s.class)
}

func (s *storageLogTransport) VerifyUpload(ctx context.Context, q blob.LogUploadCheck) error {
	s.checks++
	return (logseg.UploadVerifier{Objects: s.objects}).Verify(ctx, q, s.class)
}

type storageLostResponse struct{ http.RoundTripper }

func (h storageLostResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := h.RoundTripper.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	return nil, errors.Join(errors.New("lost object PUT response"), response.Body.Close())
}

func storageLogSegment(t *testing.T, number int, body []byte) (logseg.Segment, []byte) {
	t.Helper()
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw := compressed.Bytes()
	return logseg.Segment{
		N: number, FirstSeq: int64(number), LastSeq: int64(number), Lines: 1,
		Bytes: int64(len(body)), StoredBytes: int64(len(raw)), SHA256: string(storageDigest(raw)),
	}, raw
}

func checkStorageLogs(t *testing.T) {
	storageProperty(t, func(seed [16]byte, ordinal uint8, lost bool) bool {
		objects, dir := storageFilesystem(t)
		return checkStorageLogRoundTrip(t, objects, storageFilesystemHTTP(objects), func(key string, body []byte) {
			if err := os.WriteFile(filepath.Join(dir, key), body, 0o600); err != nil {
				t.Fatal(err)
			}
		}, seed, ordinal, lost)
	})
}

func checkStorageLogRoundTrip(t *testing.T, objects storageLogObjects, transport http.RoundTripper, corrupt func(string, []byte), seed [16]byte, ordinal uint8, lost bool) bool {
	t.Helper()
	class := logseg.ClassStandard
	if ordinal%2 == 0 {
		class = logseg.ClassFree
	}
	signing := &storageLogTransport{objects: objects, class: class}
	records, commit, actor, reopen := storageLogPersistence(t, ordinal, class)
	subject := commit.Subject
	if lost {
		transport = storageLostResponse{transport}
	}
	uploader, err := logseg.NewSubjectUploader(logseg.Data{
		Subject: subject, Transport: signing,
		HTTPClient: blob.TransferClient{Transport: transport}, Records: records, Actor: commit.Actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	var segments []logseg.Segment
	var content []byte
	for number := 1; number <= 2; number++ {
		line := []byte(hex.EncodeToString(seed[:]) + string(rune('0'+number)) + "\n")
		seg, raw := storageLogSegment(t, number, line)
		if err := uploader.PutSegment(t.Context(), seg, raw); err != nil {
			t.Fatal(err)
		}
		segments, content = append(segments, seg), append(content, line...)
	}
	manifest := logseg.Manifest{Status: logseg.StatusComplete, Segments: segments}
	if err := uploader.PutManifest(t.Context(), manifest); err != nil {
		t.Fatal(err)
	}
	if signing.signs != 1 || (lost && signing.checks != 3) || (!lost && signing.checks != 0) {
		return false
	}
	persisted, records := reopen()
	defer func() {
		if err := persisted.Close(); err != nil {
			t.Error(err)
		}
	}()
	reader := logseg.CommittedReader{Records: records, Objects: objects}
	for _, offset := range []int64{0, int64(len(content) / 2), int64(len(content) - 1), int64(len(content))} {
		var output bytes.Buffer
		result, err := reader.Read(t.Context(), actor, blob.LogReadRequest{Subject: subject, Offset: offset}, &output)
		if err != nil || !result.Complete || result.Bytes != int64(len(content))-offset || !bytes.Equal(output.Bytes(), content[offset:]) {
			return false
		}
	}
	foreign := subject
	foreign.RunID += "-other"
	if foreign.Attempt != nil {
		attempt := *foreign.Attempt
		attempt.RunID = foreign.RunID
		foreign.Attempt = &attempt
	}
	if result, err := reader.Read(t.Context(), actor, blob.LogReadRequest{Subject: foreign}, io.Discard); err == nil || result.Bytes != 0 {
		return false
	}
	view, err := records.ReadLogSegments(t.Context(), actor, blob.LogReadRequest{Subject: subject})
	if err != nil || len(view.Segments) != 2 {
		t.Fatalf("persisted log segments: %+v: %v", view, err)
	}
	corrupt(view.Segments[0].ObjectKey, []byte("corrupt"))
	var output bytes.Buffer
	result, err := reader.Read(t.Context(), actor, blob.LogReadRequest{Subject: subject}, &output)
	return err != nil && result.Bytes == 0 && output.Len() == 0 && !result.Complete
}
