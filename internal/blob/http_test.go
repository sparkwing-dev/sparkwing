package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func TestSignedDownloadForbiddenExplainsFreshLink(t *testing.T) {
	client := TransferClient{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	_, err := client.Transfer(t.Context(), SignedTransfer{URL: "https://storage.example/object?signature=secret"}, nil)
	const want = "signed blob transfer: HTTP 403: download links may expire; re-run the command to request a fresh link"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func (r roundTrip) RoundTrip(q *http.Request) (*http.Response, error) { return r(q) }

func TestSignedTransferHTTP(t *testing.T) {
	for _, status := range []int{200, 201, 302, 404, 500} {
		client := TransferClient{Transport: roundTrip(func(q *http.Request) (*http.Response, error) {
			if q.Method != http.MethodPut || q.ContentLength != 5 || q.Header.Get("X-Signed") != "value" || q.Header.Get("Authorization") != "" {
				t.Fatalf("wrong signed request: %v", q)
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"http://other.example/object"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}
		r, err := client.Transfer(t.Context(), SignedTransfer{URL: "https://storage.example/object?signature=one-object", Size: 5, Headers: map[string]string{"X-Signed": "value"}}, strings.NewReader("hello"))
		if (err == nil) != (status < 300) {
			t.Fatalf("status %d: %v", status, err)
		}
		if status == 404 && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if r != nil {
			_ = r.Close()
		}
	}
	for _, url := range []string{"file:///etc/passwd", "https://user:secret@storage.example/object", "https://storage.example/object#fragment", "//storage.example/object"} {
		if _, err := (TransferClient{}).Transfer(context.Background(), SignedTransfer{URL: url}, nil); err == nil {
			t.Fatalf("unsafe URL %q", url)
		}
	}
}

type failingTransferBody struct{ failure error }

func (b failingTransferBody) Read([]byte) (int, error) { return 0, b.failure }
func (b failingTransferBody) Close() error             { return b.failure }

func TestSignedTransferErrorsDoNotRetainCredentials(t *testing.T) {
	const signedURL = "https://bucket.s3.example/object?X-Amz-Credential=private&X-Amz-Signature=secret"
	for _, stage := range []string{"request", "read", "close"} {
		t.Run(stage, func(t *testing.T) {
			failure := fmt.Errorf("object request %s failed", signedURL)
			client := TransferClient{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				if stage == "request" {
					return nil, failure
				}
				return &http.Response{StatusCode: http.StatusOK, Body: failingTransferBody{failure: failure}, Header: make(http.Header)}, nil
			})}
			body, err := client.Transfer(t.Context(), SignedTransfer{URL: signedURL}, nil)
			if body != nil {
				if stage == "read" {
					_, err = io.ReadAll(body)
				} else {
					err = body.Close()
				}
				_ = body.Close()
			}
			if err == nil {
				t.Fatal("transfer failure succeeded")
			}
			for current := err; current != nil; current = errors.Unwrap(current) {
				if strings.ContainsAny(fmt.Sprintf("%+v", current), "?&") || strings.Contains(current.Error(), "secret") || errors.Is(current, failure) {
					t.Fatal("transfer error retained signed credentials")
				}
			}
		})
	}
}

func TestSignedTransferMetadataRejectsReusableCredentials(t *testing.T) {
	for _, headers := range []map[string]string{
		{"cookie": "session=private"}, {"Proxy-Authorization": "private"}, {"AUTHORIZATION": "  BeArEr private"},
	} {
		if err := (SignedTransfer{URL: "https://storage.example/object", Headers: headers}).Validate(); err == nil {
			t.Fatalf("accepted reusable headers: %v", headers)
		}
	}
	if err := (SignedTransfer{URL: "https://storage.example/object", Headers: map[string]string{"Authorization": "AWS4-HMAC-SHA256 one-object-signature"}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSignedTransferPreconditionRequiresReadableResponse(t *testing.T) {
	for _, broken := range []bool{false, true} {
		client := TransferClient{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			body := io.NopCloser(strings.NewReader("already exists"))
			if broken {
				body = io.NopCloser(failingTransferBody{failure: errors.New("private signed URL")})
			}
			return &http.Response{StatusCode: http.StatusPreconditionFailed, Body: body, Header: make(http.Header)}, nil
		})}
		_, err := client.Transfer(t.Context(), SignedTransfer{URL: "https://storage.example/object"}, strings.NewReader("hello"))
		if err == nil || errors.Is(err, ErrPreconditionFailed) == broken {
			t.Fatalf("broken %v: response = %v", broken, err)
		}
	}
}
