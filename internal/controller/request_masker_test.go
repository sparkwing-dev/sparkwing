package controller

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestMaskerMasksItsOwnRequestAndKeepsNothing(t *testing.T) {
	var logs bytes.Buffer
	s := New(nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	register := true
	h := withRequestLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := r.URL.Query().Get("s")
		if register {
			requestMasker(r.Context()).Register(secret)
		}
		s.writeInternalError(w, r, "open", errors.New("failed with "+secret))
	}), s.logger, func(*http.Request) string { return "/x" }, func(*http.Request) string { return "" })
	for i := range 2000 {
		logs.Reset()
		secret := fmt.Sprintf("external-secret-%032x", i)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x?s="+secret, nil))
		if strings.Contains(logs.String(), secret) || !strings.Contains(logs.String(), "failed with ***") {
			t.Fatalf("request %d log: %s", i, logs.String())
		}
	}
	register = false
	logs.Reset()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x?s=external-secret-"+fmt.Sprintf("%032x", 7), nil))
	if !strings.Contains(logs.String(), "failed with external-secret-") {
		t.Fatalf("a later request still masks an earlier request's secret, so registrations outlive their request: %s", logs.String())
	}
}
