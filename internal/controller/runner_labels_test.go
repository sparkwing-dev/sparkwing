package controller

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
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

//sparkwing:critical
func TestRunnerEnrollmentSpecValidatesLabelBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		labels  []string
		refusal string
	}{
		{name: "none", labels: []string{}},
		{name: "count boundary", labels: strings.Fields(strings.Repeat("gpu ", 32))},
		{name: "length boundary", labels: []string{strings.Repeat("a", 128)}},
		{name: "allowed characters", labels: []string{" AZaz09-_.:/= "}},
		{name: "too many", labels: strings.Fields(strings.Repeat("gpu ", 33)), refusal: "at most 32"},
		{name: "too long", labels: []string{strings.Repeat("a", 129)}, refusal: "1 to 128"},
		{name: "blank", labels: []string{" "}, refusal: "1 to 128"},
		{name: "space", labels: []string{"gpu prod"}, refusal: "ASCII letters"},
		{name: "control", labels: []string{"gpu\nprod"}, refusal: "ASCII letters"},
		{name: "unicode", labels: []string{"gpu\u202eprod"}, refusal: "ASCII letters"},
		{name: "selector", labels: []string{"gpu,prod"}, refusal: "ASCII letters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enrollment := runnerEnrollmentRequest{Name: "label-bounds", Personal: true, AllowedLabels: tc.labels}
			spec, enrollmentErr := runnerEnrollmentSpec(enrollment, capability.AuthorityRef{}, "acme")
			if tc.refusal == "" {
				if enrollmentErr != nil {
					t.Fatalf("enrollment refused valid labels: %v", enrollmentErr)
				}
			} else if enrollmentErr == nil || !strings.Contains(enrollmentErr.Error(), tc.refusal) {
				t.Fatalf("enrollment refusal = %v, want %q", enrollmentErr, tc.refusal)
			}
			if tc.refusal == "" && !slices.Equal(spec.AllowedLabels, tc.labels) {
				t.Fatalf("enrollment changed configured labels: %v, want %v", spec.AllowedLabels, tc.labels)
			}
		})
	}
}
