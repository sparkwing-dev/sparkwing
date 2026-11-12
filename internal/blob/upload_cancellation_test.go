package blob

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
)

type blockedUploadReader struct {
	*strings.Reader
	entered chan struct{}
	release <-chan struct{}
}

func (r blockedUploadReader) Read(p []byte) (int, error) {
	close(r.entered)
	<-r.release
	return r.Reader.Read(p)
}

func TestSignedUploadCancellationDoesNotWaitForBlockedRequestReader(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		release := make(chan struct{})
		readDone := make(chan struct{})
		uploadDone := make(chan struct{})
		result := make(chan error, 1)
		reader := blockedUploadReader{Reader: strings.NewReader("hello"), entered: make(chan struct{}), release: release}
		defer func() { close(release); <-readDone; <-uploadDone }()
		client := TransferClient{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			go func() {
				defer func() { _ = r.Body.Close(); close(readDone) }()
				var b [5]byte
				_, _ = r.Body.Read(b[:])
			}()
			<-reader.entered
			cancel()
			return nil, io.EOF
		})}
		go func() {
			defer close(uploadDone)
			_, err := Upload(ctx, client, SignedTransfer{URL: "https://objects.example/", Size: 5}, reader, nil)
			result <- err
		}()
		synctest.Wait()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled upload=%v", err)
			}
		default:
			t.Error("cancelled upload waited for a blocked HTTP request reader")
		}
	})
}
