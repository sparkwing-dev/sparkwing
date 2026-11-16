package blob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// TransferClient sends only the signed headers to the one-object URL. Its
// transport is injectable for filesystem signing and S3 conformance tests.
type TransferClient struct{ Transport http.RoundTripper }

func (c TransferClient) Transfer(ctx context.Context, transfer SignedTransfer, body io.Reader) (_ io.ReadCloser, resultErr error) {
	status := 0
	defer func() {
		if body != nil && resultErr != nil && status != 0 {
			failure := newTransferFailure(http.MethodPut, transfer.URL, resultErr)
			failure.class = fmt.Sprintf("HTTP %d", status)
			failure.retry = status == 502 || status == 503 || status == 504
			failure.cause = resultErr
			resultErr = failure
		}
	}()
	if err := transfer.Validate(); err != nil {
		return nil, err
	}
	method := http.MethodGet
	if body != nil {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, transfer.URL, body)
	if err != nil {
		return nil, safeTransferError(err)
	}
	for k, v := range transfer.Headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.ContentLength = transfer.Size
		if transfer.Size == 0 {
			req.Body = http.NoBody
		}
	}
	client := &http.Client{Transport: c.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		if method == http.MethodPut {
			return nil, newTransferFailure(method, transfer.URL, err)
		}
		return nil, safeTransferError(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status = response.StatusCode
		body := privateTransferBody{response.Body}
		payload, readErr := io.ReadAll(io.LimitReader(body, 4096))
		_, drainErr := io.Copy(io.Discard, body)
		if err := errors.Join(readErr, drainErr, body.Close()); err != nil {
			return nil, err
		}
		switch response.StatusCode {
		case http.StatusNotFound:
			return nil, ErrNotFound
		case http.StatusPreconditionFailed:
			return nil, ErrPreconditionFailed
		case http.StatusConflict:
			failure := &apierr.Error{Code: apierr.Conflict, Message: "signed blob transfer: HTTP 409: object conflict"}
			var envelope struct {
				Error *apierr.Error `json:"error"`
			}
			// safety: storage responses can contain credentials; retain only this fixed public reason.
			if json.Unmarshal(payload, &envelope) == nil && envelope.Error != nil && envelope.Error.Code == apierr.Conflict && envelope.Error.Message == "object integrity mismatch" {
				failure.Message = "object integrity mismatch"
			}
			return nil, failure
		default:
			if response.StatusCode == http.StatusForbidden && method == http.MethodGet {
				return nil, errors.New("signed blob transfer: HTTP 403: download links may expire; re-run the command to request a fresh link")
			}
			return nil, fmt.Errorf("signed blob transfer: HTTP %d", response.StatusCode)
		}
	}
	return privateTransferBody{response.Body}, nil
}

// ErrPreconditionFailed reports a create-only object that already exists.
// Its caller must verify the stored bytes before accepting a replay.
var ErrPreconditionFailed = errors.New("signed blob transfer: HTTP 412")

// Validate checks signed-object metadata without accepting reusable credentials.
// Expiration and object identity belong to the caller's signing contract.
func (t SignedTransfer) Validate() error {
	if t.Size < 0 {
		return ErrIntegrity
	}
	u, err := url.Parse(t.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return errors.New("invalid signed object transfer")
	}
	for name, value := range t.Headers {
		if strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Proxy-Authorization") || strings.EqualFold(name, "Authorization") && strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "bearer ") {
			return errors.New("signed object transfer contains reusable credentials")
		}
	}
	return nil
}

func safeTransferError(err error) error {
	for _, safe := range []error{nil, io.EOF, io.ErrUnexpectedEOF, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	// safety: transport errors can retain signed URLs in both text and unwrap chains.
	return errors.New("signed blob transfer failed")
}

type privateTransferBody struct{ body io.ReadCloser }

func (b privateTransferBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	return n, safeTransferError(err)
}

func (b privateTransferBody) Close() error {
	return safeTransferError(b.body.Close())
}
