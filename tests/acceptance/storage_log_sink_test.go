package acceptance_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func checkStorageFilesystemLogSink(t *testing.T) {
	storageProperty(t, func(seed [16]byte, ordinal uint8, lost bool) bool {
		objects, _ := storageFilesystem(t)
		return checkStorageLogSink(t, objects, storageObjectHTTP{objects}, seed, ordinal, lost)
	})
}

func checkStorageS3LogSink(t *testing.T) {
	storageProperty(t, func(seed [16]byte, ordinal uint8, lost bool) bool {
		objects, transport := storageS3(t)
		return checkStorageLogSink(t, objects, transport, seed, ordinal, lost)
	})
}

func checkStorageLogSink(t *testing.T, objects storageLogObjects, transport http.RoundTripper, seed [16]byte, ordinal uint8, lost bool) bool {
	t.Helper()
	subject := storageLogSubject(seed, ordinal)
	signing := &storageLogTransport{objects: objects, class: logseg.ClassStandard}
	records := &storageLogRecords{}
	if lost {
		transport = storageLostResponse{transport}
	}
	uploader, err := logseg.NewSubjectUploader(logseg.Data{
		Subject: subject, Transport: signing, Records: records,
		HTTPClient: blob.TransferClient{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	spool := t.TempDir()
	segmentBytes := 64 + int64(seed[1])
	writers, count := 2+int(ordinal%4), 2+int(seed[0]%7)
	if ordinal%8 == 0 {
		segmentBytes, count = 1, 17
	}
	var live []byte
	sink, err := logseg.NewSink(t.Context(), uploader, logseg.Options{
		Subject: subject, SpoolDir: spool, Retries: 1, SegmentBytes: segmentBytes,
		AfterFunc: func(time.Duration, func()) func() bool { return func() bool { return true } },
		Live: func(offset int64, body []byte) {
			if offset != int64(len(live)) {
				t.Errorf("live offset=%d, want=%d", offset, len(live))
			}
			live = append(live, body...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			if _, err := sink.Close(t.Context(), logseg.End{Stopped: true}); err != nil {
				t.Error(err)
			}
		}
	})
	sink.SetWatched(true)
	line := func(writer, sequence int) string { return fmt.Sprintf("%x/%d/%d", seed, writer, sequence) }
	start := make(chan struct{})
	var workers sync.WaitGroup
	for writer := range writers {
		workers.Go(func() {
			<-start
			stream := fmt.Sprint(writer)
			for sequence := 1; sequence <= count; sequence++ {
				body := []byte(line(writer, sequence))
				if sequence%2 == 0 {
					body = append(body, '\n')
				}
				for range 2 {
					if err := sink.Append(body, stream, int64(sequence), int64(sequence)); err != nil {
						t.Error(err)
						return
					}
				}
			}
			sink.WriterSealed(stream, int64(count), 0)
		})
	}
	close(start)
	workers.Wait()
	manifest, err := sink.Close(t.Context(), logseg.End{})
	closed = true
	if err != nil || manifest.Status != logseg.StatusComplete || manifest.Lines != int64(writers*count) {
		t.Fatalf("sink close: %+v: %v", manifest, err)
	}
	if ordinal%8 == 0 && signing.signs < 3 {
		t.Fatalf("signed %d batches for %d segments", signing.signs, len(manifest.Segments))
	}
	entries, err := os.ReadDir(spool)
	if err != nil || len(entries) != 0 {
		t.Fatalf("spool cleanup: %v: %v", entries, err)
	}
	reader := logseg.CommittedReader{Records: records, Objects: objects}
	var output bytes.Buffer
	result, err := reader.Read(t.Context(), store.Actor{}, blob.LogReadRequest{Subject: subject}, &output)
	if err != nil || !result.Complete || result.Bytes != int64(len(live)) || !bytes.Equal(live, output.Bytes()) {
		t.Fatalf("sink read: %+v: %v", result, err)
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != writers*count {
		return false
	}
	for writer := range writers {
		position := -1
		for sequence := 1; sequence <= count; sequence++ {
			index := slices.Index(lines, line(writer, sequence))
			if index <= position {
				return false
			}
			position = index
		}
	}
	for _, offset := range []int64{1, int64(seed[2]) % result.Bytes, result.Bytes, result.Bytes + 1} {
		output.Reset()
		tail, err := reader.Read(t.Context(), store.Actor{}, blob.LogReadRequest{Subject: subject, Offset: offset}, &output)
		if err != nil || !tail.Complete || !bytes.Equal(output.Bytes(), live[min(offset, int64(len(live))):]) {
			return false
		}
	}
	foreign := storageLogSubject(seed, ordinal)
	if foreign.Attempt != nil {
		foreign.Attempt.Generation++
	} else {
		foreign.Unit.Generation++
	}
	result, err = reader.Read(t.Context(), store.Actor{}, blob.LogReadRequest{Subject: foreign}, io.Discard)
	if !errors.Is(err, blob.ErrIntegrity) || result.Bytes != 0 {
		return false
	}
	prefix, err := blob.LogSubjectPrefix(signing.class, subject)
	if err != nil {
		t.Fatal(err)
	}
	body, err := objects.Get(t.Context(), blob.LogManifestKey(prefix))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(body)
	err = errors.Join(err, body.Close())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := logseg.ParseManifest(raw)
	return err == nil && stored.Bytes == manifest.Bytes && slices.Equal(stored.Segments, manifest.Segments) &&
		reflect.DeepEqual(stored.Subject, &subject) && stored.Status == manifest.Status && stored.Lines == manifest.Lines &&
		(!lost || signing.checks == len(manifest.Segments)+1)
}
