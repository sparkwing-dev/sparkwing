package controller

import (
	"encoding/json"
	"strings"
	"testing"
)

//sparkwing:critical
func TestRunnerEnrollmentRefusesInvalidAllowedLabels(t *testing.T) {
	for name, labels := range map[string][]string{
		"count":    strings.Fields(strings.Repeat("gpu ", 33)),
		"length":   {strings.Repeat("a", 129)},
		"space":    {"hidden label"},
		"control":  {"gpu\nprod"},
		"unicode":  {"gpu\u202eprod"},
		"selector": {"gpu,prod"},
		"empty":    {" "},
	} {
		t.Run(name, func(t *testing.T) {
			s, raw, csrf := reviewAccount(t, "owner")
			labelsJSON, err := json.Marshal(labels)
			if err != nil {
				t.Fatal(err)
			}
			request := `{"name":"bad-labels","repos":["github.com/acme/*"],"allowed_labels":` + string(labelsJSON) + `}`
			normal := reviewSessionRequest(t, s, raw, csrf, "POST", "/api/v1/runners", request, 400)
			browser := devicePost(t, s.Handler(), "/api/v1/auth/device", `{"kind":"runner","runner":{"name":"bad-labels","personal":true,"allowed_labels":`+string(labelsJSON)+`}}`, 400)
			for _, response := range []string{normal.Body.String(), browser.Body.String()} {
				if !strings.Contains(response, "labels") {
					t.Fatalf("label refusal gave no remedy: %s", response)
				}
			}
		})
	}
}
