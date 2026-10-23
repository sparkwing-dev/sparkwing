package blob

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

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
