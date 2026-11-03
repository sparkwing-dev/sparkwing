package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestDecodeEngineRequestClassifiesMalformedJSON(t *testing.T) {
	const marker = "zq7private"
	for _, tc := range []struct {
		body, want string
	}{
		{`{`, "ends before its JSON object is complete"},
		{`{"name": ` + marker + `}`, "not valid JSON at byte"},
		{`{"name": 7}`, "wrong JSON type"},
		{`{"` + marker + `": 1}`, "field this endpoint does not accept"},
	} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
		request.Header.Set("Content-Type", "application/json")
		var body struct {
			Name string `json:"name"`
		}
		err := decodeEngineRequest(request, &body)
		var api *apierr.Error
		if !errors.As(err, &api) || api.Code != apierr.BadRequest || !strings.Contains(api.Message, tc.want) {
			t.Fatalf("%q: got %v, want bad request containing %q", tc.body, err, tc.want)
		}
		if strings.Contains(api.Message, marker) || strings.Contains(api.Message, "name") {
			t.Fatalf("%q: decode message reflected request bytes: %s", tc.body, api.Message)
		}
	}
}
