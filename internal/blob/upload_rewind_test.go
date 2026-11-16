package blob

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type currentOnlyUploadReader struct {
	*strings.Reader
	rewinds int
}

func (r *currentOnlyUploadReader) Seek(offset int64, whence int) (int64, error) {
	if offset == 0 && whence == io.SeekCurrent {
		return r.Reader.Seek(offset, whence)
	}
	r.rewinds++
	return 0, errors.New("rewind https://objects.example/private?signature=rewind-secret")
}

func TestSignedUploadRefusedRewindStillGetsFirstAttempt(t *testing.T) {
	for _, failedTransfer := range []bool{false, true} {
		name := "success"
		if failedTransfer {
			name = "transport_failure"
		}
		t.Run(name, func(t *testing.T) {
			data, _, signer := newOutputData(t)
			signer.upload = func(_ context.Context, intent UploadIntent) (UploadTicket, error) {
				signer.intent = intent
				return UploadTicket{ID: "ticket", Transfer: SignedTransfer{URL: "https://objects.example/private?signature=put-secret", Digest: intent.Digest, Size: intent.Size}}, nil
			}
			puts := 0
			store := NewSignedStore(SignedStoreOptions{Signer: signer, HTTPClient: TransferClient{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				puts++
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != "hello" {
					t.Fatalf("body=%q error=%v", got, err)
				}
				if failedTransfer {
					return nil, io.EOF
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			})}})
			body := &currentOnlyUploadReader{Reader: strings.NewReader("hello")}
			_, err := store.Put(t.Context(), PutRequest{Identity: data.Identity, Authority: data.Authority, Digest: digest("hello"), Size: 5, Body: body})
			if puts != 1 {
				t.Fatalf("puts=%d error=%v; want one first attempt", puts, err)
			}
			if !failedTransfer {
				if err != nil || signer.commits != 1 || body.rewinds != 0 {
					t.Fatalf("commits=%d rewinds=%d error=%v", signer.commits, body.rewinds, err)
				}
				return
			}
			if !errors.Is(err, io.EOF) || signer.commits != 0 || body.rewinds != 1 {
				t.Fatalf("commits=%d rewinds=%d error=%v", signer.commits, body.rewinds, err)
			}
			for _, want := range []string{"PUT", "objects.example", "EOF", "attempts=1", "body rewind failed; not retried"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q in %v", want, err)
				}
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/private") || strings.Contains(err.Error(), "?") {
				t.Fatalf("signed or rewind detail leaked: %v", err)
			}
		})
	}
}
