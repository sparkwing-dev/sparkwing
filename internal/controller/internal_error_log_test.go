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
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
)

func TestServerFailuresLogMaskedCauseOnceProperty(t *testing.T) {
	property := func(input []byte) bool {
		secret := "private-" + hex.EncodeToString(input) + "/+?"
		forms := []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), url.QueryEscape(secret)}
		for _, form := range forms {
			for _, path := range []string{"policy", "legacy", "internal", "encoded", "typed", "browser", "status", "panic"} {
				var logs bytes.Buffer
				server := New(nil, slog.New(slog.NewJSONHandler(&logs, nil)))
				cause := errors.New("database unavailable: " + form)
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestMasker(r.Context()).Register(secret)
					switch path {
					case "policy":
						routepolicy.WritePolicyError(w, cause)
					case "legacy":
						writeError(w, http.StatusBadGateway, cause)
					case "internal":
						server.writeInternalError(w, r, "read plan", cause)
					case "encoded":
						routepolicy.WritePolicyError(w, &apierr.Error{Code: apierr.BadRequest, Message: form, Details: map[string]any{"invalid": make(chan int)}})
					case "typed":
						routepolicy.WritePolicyError(w, errors.Join(&apierr.Error{Code: apierr.Unavailable, Message: form, Hint: form, Details: map[string]any{"private": form, "retry_after_ms": 1000}}, cause))
					case "browser":
						tmpl := template.Must(template.New("broken").Funcs(template.FuncMap{"fail": func() (string, error) { return "", cause }}).Parse("{{fail}}"))
						renderPage(w, http.StatusOK, tmpl, nil)
					case "status":
						w.WriteHeader(http.StatusServiceUnavailable)
					case "panic":
						panic(cause)
					}
				})
				observed := withRequestLog(handler, server.logger, func(*http.Request) string { return "/api/v1/runs/{run}/plan" }, func(*http.Request) string { return "" })
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

func TestSignedUploadFailureLogsMaskedCauseOnceProperty(t *testing.T) {
	property := func(value uint64) bool {
		secret := fmt.Sprintf("private-%x", value)
		root := t.TempDir()
		objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: root, Endpoint: "http://local", SigningKey: bytes.Repeat([]byte{42}, 32)})
		if err != nil {
			t.Fatal(err)
		}
		defer objects.Close()
		signed, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: "PUT", Key: "teams/a/pending/object", Size: 1, SHA256: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		server := New(nil, slog.New(slog.NewJSONHandler(&logs, nil))).WithDataObjects(objects)
		ctx, masker := withRequestMasker(t.Context())
		masker.Register(secret)
		req := httptest.NewRequest("PUT", signed.URL, failingUploadBody{errors.New("upload read: " + secret)}).WithContext(ctx)
		req.Header.Set(RequestIDHeader, "upload-request")
		out := httptest.NewRecorder()
		server.Handler().ServeHTTP(out, req)
		records := auditLines(t, &logs, "http request failed")
		if out.Code != 500 || len(records) != 1 || records[0]["request_id"] != out.Header().Get(RequestIDHeader) || !strings.Contains(fmt.Sprint(records[0]["err"]), "upload read: ***") || !strings.Contains(fmt.Sprint(records[0]["err"]), "***") || strings.Contains(logs.String(), secret) || strings.Contains(out.Body.String(), secret) {
			t.Logf("upload HTTP %d: %s logs=%s", out.Code, out.Body, logs.String())
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 8}); err != nil {
		t.Fatal(err)
	}
}

type failingUploadBody struct{ err error }

func (r failingUploadBody) Read([]byte) (int, error) { return 0, r.err }
