package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

const admissionPath = "/api/v1/local/admission"

func (cl *Client) admissionCall(ctx context.Context, method, path string, input, output any, limit int) error {
	retry := newRetry("admission HTTP request", limit)
	for {
		err := cl.apiCall(ctx, method, admissionPath+path, input, output)
		if err == nil || !transientAPIError(err) {
			return err
		}

		if err := retry.wait(ctx, err); err != nil {
			return err
		}
	}
}

func admissionNotFound(err error) bool {
	var status *httpStatusError
	return errors.As(err, &status) && status.code == http.StatusNotFound
}

func transientAPIError(err error) bool {
	var status *httpStatusError
	if errors.As(err, &status) {
		return status.code >= http.StatusInternalServerError
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.ErrUnexpectedEOF)
}

type httpStatusError struct {
	code int
	err  error
}

func (e *httpStatusError) Error() string { return e.err.Error() }
func (e *httpStatusError) Unwrap() error { return e.err }

func (cl *Client) apiCall(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, APIBaseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Sparkwing-Version", cl.opts.Version)
	client := NewAPIClient(wingd.APISocketBeside(cl.sock))
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxAPIResponseBytes {
		return errors.New("wingd/client: controller response exceeds limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error *apierr.Error `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error != nil {
			return &httpStatusError{code: response.StatusCode, err: envelope.Error}
		}
		return &httpStatusError{code: response.StatusCode, err: fmt.Errorf("wingd/client: %s: HTTP %d: %s", path, response.StatusCode, data)}
	}
	if output != nil {
		return json.Unmarshal(data, output)
	}
	return nil
}
