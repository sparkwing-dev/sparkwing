package blob_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestCancelledUploadCannotCommit(t *testing.T) {
	for _, stage := range []string{"before_signing", "after_signing", "after_transfer"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			signed, transferred := 0, 0
			s := &signer{upload: func(_ context.Context, i blob.UploadIntent) (blob.UploadTicket, error) {
				signed++
				if stage == "after_signing" {
					cancel()
				}
				return blob.UploadTicket{ID: "ticket", Transfer: blob.SignedTransfer{Digest: i.Digest, Size: i.Size}}, nil
			}}
			client := transfer(func(_ context.Context, _ blob.SignedTransfer, body io.Reader) (io.ReadCloser, error) {
				transferred++
				if _, err := io.Copy(io.Discard, body); err != nil {
					return nil, err
				}
				cancel()
				return io.NopCloser(strings.NewReader("")), nil
			})
			if stage == "before_signing" {
				cancel()
			}
			store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s, HTTPClient: client})
			receipt, err := store.Put(ctx, blob.PutRequest{Identity: identity(), Digest: digest("hello"), Size: 5, Body: strings.NewReader("hello")})
			if !errors.Is(err, context.Canceled) || receipt != (blob.Receipt{}) || s.commits != 0 {
				t.Fatalf("cancelled upload = %+v, %v, commits %d", receipt, err, s.commits)
			}
			if stage == "before_signing" && signed != 0 || stage != "after_transfer" && transferred != 0 {
				t.Fatalf("cancelled upload advanced: signed %d, transferred %d", signed, transferred)
			}
		})
	}
}

func TestCancelledDownloadCannotVerify(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	body := &remoteBody{Reader: cancelRead{Reader: strings.NewReader("hello"), cancel: cancel}}
	s := &signer{transfer: blob.SignedTransfer{Digest: digest("hello"), Size: 5}}
	store := blob.NewSignedStore(blob.SignedStoreOptions{Signer: s, HTTPClient: transfer(func(context.Context, blob.SignedTransfer, io.Reader) (io.ReadCloser, error) {
		return body, nil
	})})
	r, err := store.Get(ctx, blob.GetRequest{Digest: digest("hello")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, r)
	if !errors.Is(err, context.Canceled) || r.Verified() {
		t.Fatalf("cancelled read = %v, verified %v", err, r.Verified())
	}
	if err := r.Close(); err != nil || !body.closed {
		t.Fatalf("cancelled stream close = %v, closed %v", err, body.closed)
	}
}

type cancelRead struct {
	io.Reader
	cancel context.CancelFunc
}

func (r cancelRead) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.cancel()
	return n, err
}
