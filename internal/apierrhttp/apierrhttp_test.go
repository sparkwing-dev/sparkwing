package apierrhttp_test

import (
	"bytes"
	"net/http/httptest"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/apierrhttp"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestWriteMatchesTheJSONStream(t *testing.T) {
	failure := &apierr.Error{Code: hosterr.RateLimited, Message: "retry later", Details: map[string]any{"retry_after_ms": 1001}}
	w := httptest.NewRecorder()
	if err := apierrhttp.Write(w, failure); err != nil {
		t.Fatal(err)
	}
	if w.Code != 429 || w.Header().Get("Retry-After") != "2" || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("HTTP error metadata: %d %v", w.Code, w.Header())
	}
	var stream bytes.Buffer
	if err := hosterr.WriteJSON(&stream, failure); err != nil || stream.String() != w.Body.String() {
		t.Fatalf("HTTP and stream errors differ: %v", err)
	}
	for _, bad := range []*apierr.Error{nil, {Code: "unknown", Message: "x"}, {Code: hosterr.Internal, Details: map[string]any{"bad": make(chan int)}}} {
		w = httptest.NewRecorder()
		if err := apierrhttp.Write(w, bad); err == nil || w.Body.Len() != 0 || len(w.Header()) != 0 {
			t.Fatal("invalid error committed a partial response")
		}
	}
}
