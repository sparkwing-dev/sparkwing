package controller

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestServerFailuresLogMaskedCauseOnceProperty(t *testing.T) {
	property := func(input []byte) bool {
		secret := "private-" + hex.EncodeToString(input) + "/+?"
		forms := []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), url.QueryEscape(secret)}
		for _, form := range forms {
			for _, path := range []string{"policy", "legacy", "internal", "encoded", "typed", "browser", "status", "panic"} {
				var logs bytes.Buffer
				server := New(nil, slog.New(slog.NewJSONHandler(&logs, nil)))
				server.masker.Register(secret)
				cause := errors.New("database unavailable: " + form)
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch path {
					case "policy":
						writePolicyError(w, cause)
					case "legacy":
						writeError(w, http.StatusBadGateway, cause)
					case "internal":
						server.writeInternalError(w, r, "read plan", cause)
					case "encoded":
						writePolicyError(w, &apierr.Error{Code: apierr.BadRequest, Message: form, Details: map[string]any{"invalid": make(chan int)}})
					case "typed":
						writePolicyError(w, errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: form, Hint: form, Details: map[string]any{"private": form, "retry_after_ms": 1000}}, cause))
					case "browser":
						tmpl := template.Must(template.New("broken").Funcs(template.FuncMap{"fail": func() (string, error) { return "", cause }}).Parse("{{fail}}"))
						renderPage(w, http.StatusOK, tmpl, nil)
					case "status":
						w.WriteHeader(http.StatusServiceUnavailable)
					case "panic":
						panic(cause)
					}
				})
				observed := withRequestLog(handler, server.logger, func(*http.Request) string { return "/api/v1/runs/{run}/plan" }, func(*http.Request) string { return "" }, server.masker)
				req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/private-path/plan", nil)
				req.Header.Set(RequestIDHeader, "request-123")
				out := httptest.NewRecorder()
				func() {
					defer func() {
						if p := recover(); p != nil && path != "panic" {
							panic(p)
						}
					}()
					observed.ServeHTTP(out, req)
				}()
				records := auditLines(t, &logs, "http request failed")
				if len(records) != 1 || records[0]["request_id"] != out.Header().Get(RequestIDHeader) || records[0]["level"] != "ERROR" || records[0]["route"] != "/api/v1/runs/{run}/plan" {
					t.Logf("%s: invalid diagnostic: %s", path, logs.String())
					return false
				}
				if path != "status" && path != "encoded" && !strings.Contains(fmt.Sprint(records[0]["err"]), "database unavailable: ***") {
					t.Logf("%s: cause lost: %s", path, logs.String())
					return false
				}
				for _, leaked := range forms {
					if strings.Contains(logs.String(), leaked) || strings.Contains(out.Body.String(), leaked) {
						t.Logf("%s: secret leaked", path)
						return false
					}
				}
				if strings.Contains(fmt.Sprint(records[0]), "private-path") {
					t.Log("raw path logged")
					return false
				}
				if path == "typed" && out.Header().Get("Retry-After") != "1" {
					t.Log("retry delay lost")
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}
