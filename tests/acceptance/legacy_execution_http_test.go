package acceptance_test

import (
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
)

func TestEngineRetiredExecutionRoutesProperty(t *testing.T) {
	paths := []struct{ method, path string }{
		{"POST", "/api/v1/triggers"},
		{"GET", "/api/v1/triggers"},
		{"GET", "/api/v1/triggers/spawned-child"},
		{"GET", "/api/v1/triggers/RUN"},
		{"POST", "/api/v1/triggers/RUN/heartbeat"},
		{"POST", "/api/v1/triggers/RUN/done"},
		{"GET", "/api/v1/runs/RUN/pending-triggers"},
		{"POST", "/api/v1/runs/RUN/events"},

		{"POST", "/api/v1/runs/RUN/nodes"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/attempt"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/start"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/finish"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/deps"},
		{"GET", "/api/v1/runs/RUN/nodes/JOB/output"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/output-upload"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/output-commit"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/log-segments"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/log-manifest"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/logs"},
		{"GET", "/api/v1/runs/RUN/nodes/JOB/logs"},
		{"GET", "/api/v1/runs/RUN/nodes/JOB/logs/stream"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/metrics"},
		{"GET", "/api/v1/runs/RUN/nodes/JOB/metrics"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/usage"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/mark-ready"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/heartbeat"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/claim"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/execution-start"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/execution-finish"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/claim/input"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/activity"},
		{"GET", "/api/v1/runs/RUN/nodes/JOB/debug-pause"},
		{"POST", "/api/v1/runs/RUN/nodes/JOB/release"},
		{"GET", "/api/v1/runs/RUN/children/CHILD/nodes/JOB/output"},
	}
	for _, browser := range []bool{false, true} {
		s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if browser {
			s.WithDashboard(controller.Dashboard{InsecureCookies: true})
		}
		h := s.Handler()
		property := func(run, job, child []byte, presentation uint8) bool {
			for _, path := range paths {
				target := strings.NewReplacer("RUN", "r"+hex.EncodeToString(run), "JOB", "j"+hex.EncodeToString(job), "CHILD", "c"+hex.EncodeToString(child)).Replace(path.path)
				r := httptest.NewRequest(path.method, "http://controller.test"+target, strings.NewReader(`{"credential":"private-body"}`))
				switch presentation % 5 {
				case 0:
					r.Header.Set("Authorization", "Bearer swct_retired")
				case 1:
					r.Header.Set("Authorization", "Bearer swk_unrecognized")
				case 2:
					r.Header.Set("Authorization", "Session private-session")
				case 3:
					r.AddCookie(&http.Cookie{Name: "sw_session", Value: "private-cookie"})
				}
				r.Header.Set("X-Sparkwing-Claim-Generation", "1")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				var envelope apierr.Envelope
				err := json.Unmarshal(w.Body.Bytes(), &envelope)
				status, code, registered := http.StatusNotFound, apierr.NotFound, false
				if path.path == "/api/v1/runs/RUN/events" {
					status, code, registered = http.StatusMethodNotAllowed, apierr.MethodNotAllowed, true
					if w.Header().Get("Allow") == "" {
						return false
					}
				}
				if err != nil || w.Code != status || (envelope.Error == nil || envelope.Error.Code != code) || strings.Contains(w.Body.String(), "private-") || s.RegisteredRoute(r) != registered || len(w.Result().Cookies()) != 0 {
					t.Logf("browser=%t route=%s %s status=%d body=%s decode=%v", browser, path.method, target, w.Code, w.Body, err)
					return false
				}
			}
			return true
		}
		if err := quick.Check(property, &quick.Config{MaxCount: 40}); err != nil {
			t.Fatal(err)
		}
	}
}
