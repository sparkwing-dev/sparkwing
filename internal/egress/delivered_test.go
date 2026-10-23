package egress

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type partialDelivery struct{ *httptest.ResponseRecorder }

func (w partialDelivery) Write(p []byte) (int, error) {
	n, _ := w.ResponseRecorder.Write(p[:min(len(p), 3)])
	return n, errors.New("disconnected")
}

func TestHandleReportsDeliveredBytesWithoutAMeter(t *testing.T) {
	for _, meter := range []*Meter{nil, New(Config{})} {
		for _, tc := range []struct {
			method string
			status int
			want   int64
		}{
			{http.MethodGet, http.StatusOK, 3},
			{http.MethodHead, http.StatusOK, 0},
			{http.MethodGet, http.StatusForbidden, 0},
			{http.MethodGet, http.StatusPartialContent, 3},
		} {
			r := httptest.NewRequest(tc.method, "/", nil)
			got := meter.Handle(partialDelivery{httptest.NewRecorder()}, r, "principal", ClassLog, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("payload"))
			}))
			if got != tc.want {
				t.Fatalf("method=%s status=%d bytes=%d, want %d", tc.method, tc.status, got, tc.want)
			}
		}
	}
}
