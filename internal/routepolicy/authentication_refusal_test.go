package routepolicy_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

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
		routepolicy.WritePolicyError(w, failure)
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
		if w.cause == nil || w.cause.Error() != reason || failure.Message != reason {
			t.Errorf("private cause was lost or changed for %q", reason)
		}
	}
	want := `{"error":{"code":"unauthenticated","message":"credential is unavailable"}}` + "\n"
	if string(firstBody) != want {
		t.Fatalf("canonical refusal=%q", firstBody)
	}
}
