package controller_test

import hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestScheduleSourceResolutionRefusals(t *testing.T) {
	const remote = "https://8.8.8.8/acme/ci.git"
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"repository refused", http.StatusUnprocessableEntity},
		{"missing ref", http.StatusUnprocessableEntity},
		{"missing git executable", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBrowserFixture(t, nil)
			switch tc.name {
			case "repository refused":
				sourcetest.Remote(t, map[string]string{remote: filepath.Join(t.TempDir(), "unknown.git")})
			case "missing ref":
				sourcetest.FixedRemote(t, remote)
			case "missing git executable":
				t.Setenv("PATH", t.TempDir())
			}
			body := `{"repo_url":"` + remote + `","branch":"missing","follow":true,"schedules":[{"pipeline":"nightly","cron":"0 3 * * *"}]}`
			response := f.serve(f.cookieRequest(http.MethodPut, "/api/v1/schedules/repos", body))
			if response.Code != tc.status {
				t.Fatalf("schedule registration: %d %s, want %d", response.Code, response.Body, tc.status)
			}
			var failure hosterr.Envelope
			if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || failure.Error == nil {
				t.Fatalf("error envelope: %s, err=%v", response.Body, err)
			}
			logs := f.logs.String()
			if tc.status == http.StatusUnprocessableEntity {
				if failure.Error.Code != apierr.PlanFailed {
					t.Errorf("source refusal code: %s", failure.Error.Code)
				}
				for _, want := range []string{"8.8.8.8/acme/ci", "check", "credentials", "ref"} {
					if !strings.Contains(failure.Error.Message, want) {
						t.Errorf("source refusal %q lacks %q", failure.Error.Message, want)
					}
				}
				if strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "level=INFO") {
					t.Errorf("source refusal log level: %s", logs)
				}
			} else if failure.Error.Code != hosterr.Internal || !strings.Contains(logs, "level=ERROR") {
				t.Errorf("server fault: %s logs=%s", response.Body, logs)
			}
			rows, err := teststore.ForTeam(t, f.st, store.DefaultTeam).ListCronSchedules(t.Context())
			if err != nil || len(rows) != 0 {
				t.Fatalf("refused source created schedules: %+v, err=%v", rows, err)
			}
		})
	}
}
