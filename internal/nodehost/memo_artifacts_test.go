package nodehost

import (
	"context"
	"io"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

type memoTestBytes struct {
	blob.Store
	body string
	err  error
	puts []blob.PutRequest
}

func (b *memoTestBytes) Get(context.Context, blob.GetRequest) (blob.VerifiedReader, error) {
	if b.err != nil {
		return nil, b.err
	}
	return &memoTestReader{Reader: strings.NewReader(b.body)}, nil
}
func (b *memoTestBytes) Put(_ context.Context, q blob.PutRequest) (blob.Receipt, error) {
	b.puts = append(b.puts, q)
	return blob.Receipt{Digest: q.Digest, Identity: q.Identity}, nil
}

type memoTestReader struct {
	*strings.Reader
	eof bool
}

func (r *memoTestReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.eof = true
	}
	return n, err
}
func (*memoTestReader) Close() error     { return nil }
func (r *memoTestReader) Verified() bool { return r.eof }
