package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestControllerNotFoundNamesTheSubjectAndExitsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		if err := hosterr.WriteJSON(w, &apierr.Error{Code: apierr.NotFound, Message: "resource not found"}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	setProfileCmdFixture(t, fmt.Sprintf("profiles:\n  far: {controller: {url: %s, token: tok}}\n", server.URL))
	for _, tc := range []struct {
		args    []string
		subject string
		list    string
	}{
		{[]string{"runs", "logs", "--run", "nope"}, `run "nope"`, "sparkwing runs list --profile 'far'"},
		{[]string{"runs", "cancel", "--run", "nope"}, `run "nope"`, "sparkwing runs list --profile 'far'"},
		{[]string{"runs", "status", "--run", "nope"}, `run "nope"`, "sparkwing runs list --profile 'far'"},
		{[]string{"secrets", "get", "--secret", "NOPE"}, `secret "NOPE"`, "sparkwing secrets list --profile 'far'"},
		{[]string{"schedules", "show", "--schedule", "nope"}, `schedule "nope"`, "sparkwing schedules list --profile 'far'"},
		{[]string{"subscriptions", "show", "--subscription", "sub_nope"}, `subscription "sub_nope"`, "sparkwing subscriptions list --profile 'far'"},
	} {
		name := strings.Join(tc.args, " ")
		for _, format := range []string{"plain", "json"} {
			args := append(append([]string{}, tc.args...), "--profile", "far", "-o", format)
			failure := runSparkwing(args)
			if failure == nil {
				t.Fatalf("%s: missing subject accepted", name)
			}
			if code := exitCodeFor(failure); code != exitRefused {
				t.Errorf("%s -o %s: exit %d, want %d: %v", name, format, code, exitRefused, failure)
			}
			var out bytes.Buffer
			writeFailure(&out, failure)
			text := out.String()
			if format == "json" {
				var envelope hosterr.Envelope
				if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || envelope.Error == nil {
					t.Fatalf("%s: envelope %s: %v", name, text, err)
				}
				if envelope.Error.Code != apierr.NotFound {
					t.Errorf("%s: code %s", name, envelope.Error.Code)
				}
				text = envelope.Error.Message + "\n" + envelope.Error.Hint
			}
			if strings.Contains(text, "/api/") || strings.Contains(text, "GET ") || strings.Contains(text, "POST ") {
				t.Errorf("%s -o %s leaked the route: %s", name, format, text)
			}
			if !strings.Contains(text, tc.subject+" not found") || !strings.Contains(text, tc.list) {
				t.Errorf("%s -o %s: want %q and %q in %s", name, format, tc.subject+" not found", tc.list, text)
			}
		}
	}
}
