package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestOutputUploadRecoversAfterServerClosesFirstPut(t *testing.T) {
	var puts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := puts.Add(1)
		if r.Method != http.MethodPut {
			t.Errorf("method=%s", r.Method)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if string(body) != `"linux/amd64"` {
			t.Errorf("attempt=%d body=%q", n, body)
		}
		if n == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	data, records, signer := newOutputData(t)
	tickets := 0
	signer.upload = func(_ context.Context, i UploadIntent) (UploadTicket, error) {
		tickets++
		signer.intent = i
		return UploadTicket{ID: "ticket", Transfer: SignedTransfer{URL: server.URL + "/object?signature=private", Digest: i.Digest, Size: i.Size}}, nil
	}
	data.Bytes = NewSignedStore(SignedStoreOptions{Signer: signer})
	record, err := data.Put(t.Context(), []byte(`"linux/amd64"`))
	if err != nil {
		t.Fatalf("recoverable signed PUT failed instead of retrying: %v", err)
	}
	if puts.Load() != 2 || tickets != 1 || signer.commits != 1 || records.request.Digest != record.Digest {
		t.Fatalf("puts=%d tickets=%d commits=%d record=%+v", puts.Load(), tickets, signer.commits, record)
	}
}

func TestOutputUploadExhaustionReportsSafeTransportDetails(t *testing.T) {
	data, _, signer := newOutputData(t)
	var puts int
	signer.upload = func(_ context.Context, i UploadIntent) (UploadTicket, error) {
		signer.intent = i
		return UploadTicket{ID: "ticket", Transfer: SignedTransfer{URL: "https://objects.example/object?X-Amz-Signature=private-signature", Digest: i.Digest, Size: i.Size}}, nil
	}
	data.Bytes = NewSignedStore(SignedStoreOptions{Signer: signer, HTTPClient: TransferClient{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		puts++
		_, _ = io.Copy(io.Discard, r.Body)
		return nil, io.EOF
	})}})
	_, err := data.Put(t.Context(), []byte(`"linux/amd64"`))
	if err == nil || !errors.Is(err, io.EOF) || puts != 3 || signer.commits != 0 {
		t.Fatalf("puts=%d commits=%d error=%v", puts, signer.commits, err)
	}
	for _, want := range []string{"PUT", "objects.example", "EOF", "3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(current.Error(), "private-signature") || strings.Contains(current.Error(), "?") {
			t.Fatalf("signed query escaped: %v", current)
		}
	}
}

func TestOutputUploadForbiddenIsNotRetried(t *testing.T) {
	data, _, signer := newOutputData(t)
	puts := 0
	signer.upload = func(_ context.Context, i UploadIntent) (UploadTicket, error) {
		signer.intent = i
		return UploadTicket{ID: "ticket", Transfer: SignedTransfer{URL: "https://objects.example/object?signature=private", Digest: i.Digest, Size: i.Size}}, nil
	}
	data.Bytes = NewSignedStore(SignedStoreOptions{Signer: signer, HTTPClient: TransferClient{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		puts++
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("private body")), Header: make(http.Header)}, nil
	})}})
	_, err := data.Put(t.Context(), []byte(`"linux/amd64"`))
	if err == nil || puts != 1 || signer.commits != 0 || !strings.Contains(err.Error(), "403") {
		t.Fatalf("puts=%d commits=%d error=%v", puts, signer.commits, err)
	}
}

func TestSignedUploadStreamingBodyIsNotRetried(t *testing.T) {
	data, _, signer := newOutputData(t)
	puts := 0
	signer.upload = func(_ context.Context, i UploadIntent) (UploadTicket, error) {
		signer.intent = i
		return UploadTicket{ID: "ticket", Transfer: SignedTransfer{URL: "https://objects.example/?signature=secret", Digest: i.Digest, Size: i.Size}}, nil
	}
	store := NewSignedStore(SignedStoreOptions{Signer: signer, HTTPClient: TransferClient{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		puts++
		_, _ = io.Copy(io.Discard, r.Body)
		return nil, io.EOF
	})}})
	_, err := store.Put(t.Context(), PutRequest{Identity: data.Identity, Authority: data.Authority, Digest: digest("hello"), Size: 5, Body: struct{ io.Reader }{strings.NewReader("hello")}})
	if err == nil || puts != 1 || !strings.Contains(err.Error(), "not replayable; not retried") || signer.commits != 0 {
		t.Fatalf("puts=%d commits=%d error=%v", puts, signer.commits, err)
	}
}

func TestSignedUploadRenewsExpiredTicketBeforeCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		data, records, signer := newOutputData(t)
		tickets := 0
		signer.upload = func(_ context.Context, i UploadIntent) (UploadTicket, error) {
			tickets++
			signer.intent = i
			expiry := time.Now().Add(time.Minute)
			if tickets == 1 {
				expiry = time.Now().Add(50 * time.Millisecond)
			}
			return UploadTicket{ID: fmt.Sprint(tickets), Transfer: SignedTransfer{URL: "https://objects.example/?ticket=" + fmt.Sprint(tickets), Digest: i.Digest, Size: i.Size, ExpiresAt: expiry}}, nil
		}
		signer.commit = func(_ context.Context, c CommitUpload) (Receipt, error) {
			if c.TicketID != "2" {
				t.Errorf("committed expired ticket=%s", c.TicketID)
			}
			return Receipt{Identity: data.Identity, Digest: digest("hello")}, nil
		}
		puts := 0
		data.Bytes = NewSignedStore(SignedStoreOptions{Signer: signer, HTTPClient: TransferClient{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			puts++
			if r.URL.Query().Get("ticket") != fmt.Sprint(puts) {
				t.Error("did not refresh expired ticket")
			}
			_, _ = io.Copy(io.Discard, r.Body)
			if puts == 1 {
				return nil, io.EOF
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
		})}})
		if _, err := data.Put(t.Context(), []byte("hello")); err != nil {
			t.Fatal(err)
		}
		if tickets != 2 || puts != 2 || signer.commits != 1 || records.request.Digest != digest("hello") {
			t.Fatalf("tickets=%d puts=%d commits=%d", tickets, puts, signer.commits)
		}
	})
}

func TestSignedUploadHTTPStatusRetryPolicy(t *testing.T) {
	for _, status := range []int{403, 500, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			puts := 0
			client := TransferClient{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				puts++
				_, _ = io.Copy(io.Discard, r.Body)
				code := 200
				if puts == 1 {
					code = status
				}
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private response"))}, nil
			})}
			response, err := Upload(t.Context(), client, SignedTransfer{URL: "https://objects.example/?signature=private", Size: 5}, strings.NewReader("hello"), nil)
			if response != nil {
				_ = response.Close()
			}
			retry := status == 502 || status == 503 || status == 504
			if retry && (err != nil || puts != 2) || !retry && (err == nil || puts != 1) {
				t.Fatalf("status=%d puts=%d error=%v", status, puts, err)
			}
		})
	}
}

func TestSignedUploadTransportDiagnosticsDiscardSignedURL(t *testing.T) {
	const signed = "https://objects.example/private-path?X-Amz-Credential=private&X-Amz-Signature=secret"
	original := &url.Error{Op: "Put", URL: signed, Err: fmt.Errorf("private diagnostic: %w", syscall.ECONNRESET)}
	puts := 0
	client := TransferClient{Transport: roundTrip(func(*http.Request) (*http.Response, error) { puts++; return nil, original })}
	_, err := Upload(t.Context(), client, SignedTransfer{URL: signed, Size: 5}, strings.NewReader("hello"), nil)
	if puts != 3 || err == nil || !strings.Contains(err.Error(), "connection_reset") || !strings.Contains(err.Error(), "objects.example") {
		t.Fatalf("puts=%d error=%v", puts, err)
	}
	if errors.Is(err, original) {
		t.Fatal("retained original transport error")
	}
	for _, secret := range []string{"secret", "private", "X-Amz", "?"} {
		if strings.Contains(fmt.Sprintf("%+v", err), secret) {
			t.Fatalf("leaked %s: %v", secret, err)
		}
	}
}

func TestSignedUploadCancellationStopsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	puts := 0
	client := TransferClient{Transport: roundTrip(func(*http.Request) (*http.Response, error) { puts++; cancel(); return nil, io.EOF })}
	_, err := Upload(ctx, client, SignedTransfer{URL: "https://objects.example/", Size: 5}, strings.NewReader("hello"), nil)
	if puts != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("puts=%d error=%v", puts, err)
	}
}
