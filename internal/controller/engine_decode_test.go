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
		{`{"` + marker + `-Key": 1}`, "field this endpoint does not accept; upgrade the controller"},
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

func TestDecodeEngineRequestNamesAnUnknownIdentifierField(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x","agent":"codex"}`))
	request.Header.Set("Content-Type", "application/json")
	var body struct {
		Name string `json:"name"`
	}
	err := decodeEngineRequest(request, &body)
	var api *apierr.Error
	if !errors.As(err, &api) || api.Code != apierr.BadRequest || api.Details[apierr.DetailUnknownField] != "agent" ||
		!strings.Contains(api.Message, `field "agent"`) || !strings.Contains(api.Message, "upgrade the controller") {
		t.Fatalf("got %#v, want a bad request naming the agent field and the upgrade", err)
	}
	if strings.Contains(api.Message, "codex") {
		t.Fatalf("decode message reflected a request value: %s", api.Message)
	}
}
