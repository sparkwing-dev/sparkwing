package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type engineClient struct {
	base     string
	http     *http.Client
	bearer   func(context.Context) (string, error)
	launcher bool
}

func (c *engineClient) request(ctx context.Context, method, pattern, path, key string, input, output any) error {
	policy, ok := capability.LookupRoute(method, pattern)
	if !ok {
		return &apierr.Error{Code: apierr.PolicyMissing, Message: "controller route has no policy"}
	}
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode controller request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create controller request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if c.bearer != nil {
		bearer, err := c.bearer(ctx)
		if err != nil {
			return err
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
	}
	mayRetry := policy.Retry == capability.RetrySafe || policy.Retry == capability.RetryExactBody || policy.Retry == capability.RetryIdempotencyKey && key != ""
	// safety: intake replay cannot recover a lost one-time here bearer.
	switch input := input.(type) {
	case controllerwire.CreateRunRequest:
		mayRetry = mayRetry && input.Request.Options.Runner.Value.Kind != runspec.RunnerHere
	case controllerwire.RetryRequest:
		// safety: the source run's sealed runner selection is unavailable at this boundary.
		mayRetry = false
	}
	for attempt := 0; ; attempt++ {
		resp, requestErr := c.http.Do(req)
		if requestErr != nil {
			if !mayRetry || attempt >= UnavailableRetries || ctx.Err() != nil || !transientExecutionStartError(requestErr) {
				return requestErr
			}
			if err := waitFor(ctx, time.Duration(1<<attempt)*100*time.Millisecond); err != nil {
				return err
			}
		} else {
			retry, err := c.readResponse(resp, output)
			if !mayRetry || attempt >= UnavailableRetries || !retry {
				return err
			}
			wait, ok := parseRetryAfter(resp)
			if !ok {
				return err
			}
			if err := waitFor(ctx, wait); err != nil {
				return err
			}
		}
		next, err := rewind(req)
		if err != nil {
			return fmt.Errorf("replay controller request: %w", err)
		}
		req = next
	}
}

func (c *engineClient) readResponse(resp *http.Response, output any) (bool, error) {
	data, err := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if err != nil {
		return false, fmt.Errorf("read controller response: %w", err)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close controller response: %w", closeErr)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return false, &apierr.Error{Code: apierr.ProtocolError, Message: "controller API redirects are refused"}
	}
	fields, err := responseObject(data)
	if err != nil {
		return false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		inner, ok := fields["error"]
		if !ok {
			return false, fmt.Errorf("controller response is missing its error envelope")
		}
		failure, err := apierr.DecodeInner(inner)
		if err != nil {
			return false, fmt.Errorf("decode controller error: %w", err)
		}
		if failure == nil {
			return false, fmt.Errorf("controller error is null")
		}
		retry := resp.StatusCode == http.StatusServiceUnavailable && failure.Code == apierr.Unavailable || resp.StatusCode == http.StatusTooManyRequests && failure.Code == apierr.RateLimited
		return retry, failure
	}
	// safety: recovery carries a persisted inner error as part of its successful result.
	if _, exists := fields["error"]; exists && reflect.TypeOf(output).Elem() != reflect.TypeFor[nodeproto.JobFailure]() {
		return false, fmt.Errorf("successful controller response contains an error")
	}
	if err := requiredResponseFields(fields, reflect.TypeOf(output).Elem()); err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, output); err != nil {
		return false, fmt.Errorf("decode controller response: %w", err)
	}
	return false, nil
}

func responseObject(data []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, fmt.Errorf("controller response must be a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("malformed controller response")
		}
		name, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("malformed controller response field")
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, fmt.Errorf("duplicate controller response field")
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return nil, fmt.Errorf("malformed controller response value")
		}
		fields[name] = raw
	}
	if _, err := d.Token(); err != nil {
		return nil, fmt.Errorf("incomplete controller response")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing controller response data")
	}
	return fields, nil
}

func requiredResponseFields(fields map[string]json.RawMessage, typ reflect.Type) error {
	for n := 0; n < typ.NumField(); n++ {
		f := typ.Field(n)
		name, options, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Anonymous && name == "" {
			if err := requiredResponseFields(fields, f.Type); err != nil {
				return err
			}
			continue
		}
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		// safety: encoding/json otherwise decodes case-folded keys that skipped validation.
		for key := range fields {
			if key != name && strings.EqualFold(key, name) {
				return fmt.Errorf("controller response has a noncanonical field name")
			}
		}
		raw, ok := fields[name]
		if !ok && !strings.Contains(options, "omitempty") && !strings.Contains(options, "omitzero") {
			return fmt.Errorf("controller response is missing %s", name)
		}
		if ok {
			if err := responseValue(raw, f.Type); err != nil {
				return fmt.Errorf("controller response field %s: %w", name, err)
			}
		}
	}
	return nil
}

func responseValue(raw json.RawMessage, typ reflect.Type) error {
	if typ == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
			return nil
		default:
			return fmt.Errorf("null is not a %s", typ.Kind())
		}
	}
	if typ == reflect.TypeFor[apierr.Error]() {
		_, err := apierr.DecodeInner(raw)
		return err
	}
	if reflect.PointerTo(typ).Implements(reflect.TypeFor[json.Unmarshaler]()) {
		return nil
	}
	switch typ.Kind() {
	case reflect.Pointer:
		return responseValue(raw, typ.Elem())
	case reflect.Struct:
		fields, err := responseObject(raw)
		if err != nil {
			return err
		}
		return requiredResponseFields(fields, typ)
	case reflect.Map:
		fields, err := responseObject(raw)
		if err != nil {
			return err
		}
		for _, value := range fields {
			if err := responseValue(value, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		// safety: byte slices use encoding/json's base64 string representation.
		if typ.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return fmt.Errorf("expected an array")
		}
		for _, value := range values {
			if err := responseValue(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
