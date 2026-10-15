package nodehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

type sourceTransfer func(*http.Request) (*http.Response, error)

func (f sourceTransfer) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type sourceReadError struct{ err error }

func (r sourceReadError) Read([]byte) (int, error) { return 0, r.err }

type sourceCloseError struct {
	io.ReadCloser
	err error
}

func (r sourceCloseError) Close() error { return errors.Join(r.ReadCloser.Close(), r.err) }

func TestSourceBundleFailures(t *testing.T) {
	t.Helper()
	spec, options, adapters, ledger := sourceAcquisitionFixture(t)
	spec.Request.Request.Source.BundleDigest.Set = true
	for _, mode := range []string{"checksum", "invalid-bundle", "missing", "redirect", "download", "read", "close", "cancel", "lease-loss"} {
		t.Run(mode, func(t *testing.T) {
			adapters.trace = nil
			sealed := spec
			digest := sha256.Sum256(adapters.bundle)
			adapters.digest = hex.EncodeToString(digest[:])
			body := adapters.bundle
			if mode == "invalid-bundle" {
				body = []byte("not a git bundle")
				digest = sha256.Sum256(body)
				adapters.digest = hex.EncodeToString(digest[:])
			}
			sealed.Request.Request.Source.BundleDigest.Value = adapters.digest
			projected, err := source.ForExecution(sealed)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause := error(context.Canceled)
			if mode == "lease-loss" {
				cause = lease.ErrLeaseLost
			}
			calls, closed := 0, adapters.closed
			options.BundleTransport = sourceTransfer(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls != 1 {
					return nil, errors.New("source followed a redirect")
				}
				if mode == "download" {
					return nil, errors.New("failed " + sourceBundleURL)
				}
				response, err := adapters.RoundTrip(request)
				if err != nil {
					return response, err
				}
				reader := response.Body.(sourceBundleReader)
				reader.Reader = bytes.NewReader(body)
				switch mode {
				case "checksum":
					reader.Reader = strings.NewReader("different bytes")
				case "missing":
					response.StatusCode = http.StatusNotFound
				case "redirect":
					response.StatusCode = http.StatusFound
					response.Header.Set("Location", "https://other.example.test/leak")
				case "read":
					reader.Reader = sourceReadError{err: errors.New("read failed " + sourceBundleURL)}
				case "cancel", "lease-loss":
					cancel(cause)
					reader.Reader = sourceReadError{err: ctx.Err()}
				}
				response.Body = reader
				if mode == "close" {
					response.Body = sourceCloseError{ReadCloser: reader, err: errors.New("close failed " + sourceBundleURL)}
				}
				return response, nil
			})
			entry, err := source.Acquire(ctx, projected, options)
			if err == nil || entry != nil || slices.Contains(adapters.trace, "ensure") || slices.Contains(adapters.trace, "install") {
				t.Fatalf("invalid bundle reached engine or installer: entry=%v, trace=%v, error=%v", entry, adapters.trace, err)
			}
			if strings.Contains(err.Error(), sourceBundleURL) {
				t.Fatal("source transfer error exposed its signed URL")
			}
			if (mode == "cancel" || mode == "lease-loss") && !errors.Is(err, cause) {
				t.Fatalf("cancellation lost its cause: %v", err)
			}
			wantClosed := 1
			if mode == "download" {
				wantClosed = 0
			}
			if calls != 1 || adapters.closed-closed != wantClosed {
				t.Fatalf("transfers=%d, closed=%d, want 1/%d", calls, adapters.closed-closed, wantClosed)
			}
			remaining, err := os.ReadDir(options.Workspace)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("failed source left owned files: %v, %v", remaining, err)
			}
		})
	}
	assertSourceCredentialCleanup(t, ledger)
}
