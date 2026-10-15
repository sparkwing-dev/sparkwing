package apierr_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestDecodeInner(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `{}`, `{"code":"forbidden"}`, `{"code":null,"message":"x"}`,
		`{"code":"","message":"x"}`, `{"code":4,"message":"x"}`, `{"code":"x","message":null}`,
		`{"code":"x","message":"x","hint":null}`, `{"code":"x","message":"x","details":[]}`,
		`{"code":"x","message":"x","details":null}`, `{"error":{"code":"x","message":"x"}}`,
		`{"code":"x","message":"x","error":{}}`, `{"code":"x","code":"y","message":"x"}`,
		`{"code":"x","message":"x","details":{"reason":1,"reason":2}}`,
		`{"code":"x","message":"x"} {}`, `{"code":"x","message":"x"} garbage`,
	} {
		t.Run(body, func(t *testing.T) {
			if _, err := apierr.DecodeInner([]byte(body)); err == nil {
				t.Fatal("malformed inner error accepted")
			}
		})
	}
	got, err := apierr.DecodeInner([]byte(`{"code":"future_code","message":"future error","hint":"retry","details":{"retry_after_ms":1001},"future":{"enabled":true}}`))
	if err != nil || got.Code != "future_code" || got.Hint != "retry" {
		t.Fatalf("additive error: %+v, %v", got, err)
	}
}

func TestErrorUnmarshalUsesInnerCodec(t *testing.T) {
	for _, body := range []string{`{}`, `{"code":"x"}`, `{"code":"x","code":"y","message":"x"}`, `{"error":{"code":"x","message":"x"}}`} {
		var failure apierr.Error
		if err := json.Unmarshal([]byte(body), &failure); err == nil {
			t.Fatalf("unmarshal accepted invalid inner error: %s", body)
		}
	}
	var failure apierr.Error
	if err := json.Unmarshal([]byte(`{"code":"future_code","message":"readable","future":true}`), &failure); err != nil || failure.Code != "future_code" {
		t.Fatalf("additive inner error = %+v, %v", failure, err)
	}
}

func TestErrorWriters(t *testing.T) {
	failure := &apierr.Error{Code: apierr.RateLimited, Message: "retry later", Details: map[string]any{"retry_after_ms": 1001}}
	w := httptest.NewRecorder()
	if err := apierr.WriteHTTP(w, failure); err != nil {
		t.Fatal(err)
	}
	if w.Code != 429 || w.Header().Get("Retry-After") != "2" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("HTTP error metadata: %d %v", w.Code, w.Header())
	}
	var stream bytes.Buffer
	if err := apierr.WriteJSON(&stream, failure); err != nil {
		t.Fatal(err)
	}
	if stream.String() != w.Body.String() || strings.Count(stream.String(), "\n") != 1 {
		t.Fatal("HTTP and stream errors differ")
	}
	var envelope apierr.Envelope
	if err := json.Unmarshal(stream.Bytes(), &envelope); err != nil || envelope.Error.Code != apierr.RateLimited {
		t.Fatalf("envelope: %+v, %v", envelope, err)
	}
	if err := apierr.WriteJSON(failingWriter{}, failure); err == nil {
		t.Fatal("writer failure hidden")
	}
	for _, bad := range []*apierr.Error{nil, {Code: "unknown", Message: "x"}, {Code: apierr.Internal, Details: map[string]any{"bad": make(chan int)}}} {
		w = httptest.NewRecorder()
		if err := apierr.WriteHTTP(w, bad); err == nil || w.Body.Len() != 0 || len(w.Header()) != 0 {
			t.Fatal("invalid error committed a partial response")
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }
