package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDashboardDoesNotServeServiceProbes(t *testing.T) {
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("dashboard forwarded a removed service probe route")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer controller.Close()

	for _, opts := range []HandlerOptions{
		{},
		{ControllerURL: controller.URL},
	} {
		handler := HandlerFromOptionsWithBundle(opts, authTestBundle)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/health/services", nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusNotImplemented {
			t.Errorf("service probe response = %d %s, want unavailable", rec.Code, rec.Body.String())
		}
	}
}
