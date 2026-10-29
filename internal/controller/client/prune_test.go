package client

import (
	"net/http"
	"strings"
	"testing"
)

func TestRunPrunerUsesAuthorizedDelete(t *testing.T) {
	c := testEngine(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodDelete || req.URL.Path != "/api/v1/runs/412" {
			t.Fatalf("request: %s %s", req.Method, req.URL)
		}
		return engineResponse(204, ""), nil
	})
	if err := c.DeleteRun(t.Context(), "412"); err != nil {
		t.Fatal(err)
	}
}

func FuzzRunPrunerRequiresEmptyAcknowledgement(f *testing.F) {
	for _, body := range []string{"", " ", `{}`, `{"run_id":"r1","deleted":true}`, `{"error":{"code":"conflict","message":"active run"}}`, "null", "<html>"} {
		for _, status := range []uint16{200, 202, 204, 301, 401, 403, 409, 500} {
			f.Add(status, body)
		}
	}
	f.Fuzz(func(t *testing.T, status uint16, body string) {
		if status < 100 || status > 599 || len(body) > 4096 {
			t.Skip()
		}
		c := testEngine(t, func(*http.Request) (*http.Response, error) {
			return engineResponse(int(status), body), nil
		})
		err := c.DeleteRun(t.Context(), "412")
		wantAck := status == http.StatusNoContent && strings.TrimSpace(body) == ""
		if (err == nil) != wantAck {
			t.Fatalf("HTTP %d body %q: acknowledged=%t want=%t (%v)", status, body, err == nil, wantAck, err)
		}
	})
}
