package controller

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
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
func TestRunnerEnrollmentSpecAndDeviceLoginValidateLabelBounds(t *testing.T) {
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
			device := deviceLoginRequest{Kind: controllerwire.DeviceLoginRunner, Runner: &enrollment}
			deviceErr := validateDeviceLoginRequest(&device)
			for seam, err := range map[string]error{"runnerEnrollmentSpec": enrollmentErr, "validateDeviceLoginRequest": deviceErr} {
				if tc.refusal == "" {
					if err != nil {
						t.Fatalf("%s refused valid labels: %v", seam, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), tc.refusal) {
					t.Fatalf("%s refusal = %v, want %q", seam, err, tc.refusal)
				}
			}
			if tc.refusal == "" && !slices.Equal(spec.AllowedLabels, tc.labels) {
				t.Fatalf("enrollment changed configured labels: %v, want %v", spec.AllowedLabels, tc.labels)
			}
		})
	}
}
