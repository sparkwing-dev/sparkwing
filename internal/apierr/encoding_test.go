package apierr_test

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
	"strings"
	"testing"
)

func TestErrorWriters(t *testing.T) {
	failure := &apierr.Error{Code: hosterr.RateLimited, Message: "retry later", Details: map[string]any{"retry_after_ms": 1001}}
	var stream bytes.Buffer
	if err := hosterr.WriteJSON(&stream, failure); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stream.String(), "\n") != 1 || hosterr.RetryAfter(failure) != "2" {
		t.Fatalf("stream error %q, retry after %q", stream.String(), hosterr.RetryAfter(failure))
	}
	var envelope hosterr.Envelope
	if err := json.Unmarshal(stream.Bytes(), &envelope); err != nil || envelope.Error.Code != hosterr.RateLimited {
		t.Fatalf("envelope: %+v, %v", envelope, err)
	}
	if err := hosterr.WriteJSON(failingWriter{}, failure); err == nil {
		t.Fatal("writer failure hidden")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }
