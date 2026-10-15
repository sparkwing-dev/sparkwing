package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/quick"
)

func TestRetiredLogPersistenceRoutesHaveNoWriterProperty(t *testing.T) {
	mux := (&Server{}).routers()
	property := func(run, job uint64) bool {
		for _, operation := range []string{"log-segments", "log-manifest"} {
			path := fmt.Sprintf("/api/v1/runs/%x/nodes/%x/%s", run, job, operation)
			request := httptest.NewRequest(http.MethodPost, path, nil)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Logf("retired writer %s returned %d", path, response.Code)
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}
