package blob

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// TransferClient sends only the signed headers to the one-object URL. Its
// transport is injectable for filesystem signing and S3 conformance tests.
type TransferClient struct{ Transport http.RoundTripper }

func (c TransferClient) Transfer(ctx context.Context, transfer SignedTransfer, body io.Reader) (io.ReadCloser, error) {
	if transfer.Size < 0 {
		return nil, ErrIntegrity
	}
	u, err := url.Parse(transfer.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("invalid signed blob URL")
	}
	method := http.MethodGet
	if body != nil {
		method = http.MethodPut
	}
	req, err := http.NewRequestWithContext(ctx, method, transfer.URL, body)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		if response.StatusCode == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("signed blob transfer: HTTP %d", response.StatusCode)
	}
	return response.Body, nil
}
