package apierrhttp_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
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

type causeRecorder struct {
	*httptest.ResponseRecorder
	cause error
}

func (w *causeRecorder) ResponseError(err error) { w.cause = err }

func TestUnauthenticatedResponsesDoNotRevealCredentialHistory(t *testing.T) {
	reasons := []string{"credential is unavailable", "authority has ended", "credential audience does not match", "credential epoch has ended", "membership is unavailable", "credential membership authority has ended"}
	var firstBody []byte
	var firstHeaders http.Header
	for i, reason := range reasons {
		failure := &apierr.Error{Code: hosterr.Unauthenticated, Message: reason}
		if i > 0 {
			failure.Hint = "private repair"
			failure.Details = map[string]any{"retry_after_ms": 1000, "state": reason}
		}
		w := &causeRecorder{ResponseRecorder: httptest.NewRecorder()}
		if err := apierrhttp.Write(w, failure); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d", w.Code)
		}
		if i == 0 {
			firstBody = append([]byte(nil), w.Body.Bytes()...)
			firstHeaders = w.Header().Clone()
		}
		if !bytes.Equal(w.Body.Bytes(), firstBody) || !reflect.DeepEqual(w.Header(), firstHeaders) {
			t.Errorf("%q revealed history: %s %v", reason, w.Body, w.Header())
		}
		if w.cause != failure || failure.Message != reason {
			t.Errorf("private cause was lost or changed for %q", reason)
		}
	}
	want := `{"error":{"code":"unauthenticated","message":"credential is unavailable"}}` + "\n"
	if string(firstBody) != want {
		t.Fatalf("canonical refusal=%q", firstBody)
	}
}
