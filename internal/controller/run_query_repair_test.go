package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestRunListLimitRefusalIncludesRepairExample(t *testing.T) {
	fixture, options, _, _ := sourceUploadFixture(t)
	fixture.WithEngineController(NewEngineController(fixture.store, EngineController{}))
	server := httptest.NewServer(fixture.Handler())
	defer server.Close()
	bearer, err := options.Bearer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []string{"0", "-1", "invalid", ""} {
		t.Run("limit="+limit, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/runs?limit="+limit, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+bearer)
			request.Header.Set("Accept", "application/json")
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var body struct {
				Error *apierr.Error `json:"error"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusBadRequest || body.Error == nil || body.Error.Code != apierr.BadRequest {
				t.Fatalf("refusal: %d %+v", response.StatusCode, body)
			}
			if !strings.Contains(body.Error.Message+body.Error.Hint, "limit=1") {
				t.Errorf("refusal lacks usable limit example: %+v", body.Error)
			}
		})
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/runs?limit=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Accept", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("suggested limit refused: %d", response.StatusCode)
	}
}
