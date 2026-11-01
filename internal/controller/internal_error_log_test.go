package controller

import (
	"bytes"
	"context"
	"database/sql/driver"
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

func TestRequestCancellationLogLevel(t *testing.T) {
	for _, writer := range []string{"policy", "legacy"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, tc := range []struct {
				name       string
				ctxErr     error
				cause      error
				level      string
				wantStatus int
			}{
				{"client canceled", context.Canceled, fmt.Errorf("read runs: %w", context.Canceled), "INFO", statusClientClosedRequest},
				{"connection closed by cancellation", context.Canceled, fmt.Errorf("read snapshot: %w", driver.ErrBadConn), "INFO", statusClientClosedRequest},
				{"live bad connection", nil, driver.ErrBadConn, "ERROR", http.StatusInternalServerError},
				{"bad connection after deadline", context.DeadlineExceeded, driver.ErrBadConn, "ERROR", http.StatusInternalServerError},
				{"live request", nil, context.Canceled, "ERROR", http.StatusInternalServerError},
				{"server fault after cancellation", context.Canceled, errors.New("database unavailable"), "ERROR", http.StatusInternalServerError},
				{"deadline", context.DeadlineExceeded, context.DeadlineExceeded, "ERROR", http.StatusInternalServerError},
			} {
				t.Run(writer+"/"+method+"/"+tc.name, func(t *testing.T) {
					ctx := t.Context()
					if errors.Is(tc.ctxErr, context.Canceled) {
						var cancel context.CancelFunc
						ctx, cancel = context.WithCancel(ctx)
						cancel()
					} else if errors.Is(tc.ctxErr, context.DeadlineExceeded) {
						var cancel context.CancelFunc
						ctx, cancel = context.WithDeadline(ctx, time.Time{})
						defer cancel()
					}
					path := "/api/v1/runs/{run}/local-host/snapshot"
					if method == http.MethodPost {
						path = "/api/v1/claims/{claim}/heartbeat"
					}
					h, logs := auditHarness(t, path, func(w http.ResponseWriter, _ *http.Request) {
						if writer == "legacy" {
							writeError(w, http.StatusInternalServerError, tc.cause)
						} else {
							routepolicy.WritePolicyError(w, tc.cause)
						}
					})
					out := httptest.NewRecorder()
					h.ServeHTTP(out, httptest.NewRequest(method, path, nil).WithContext(ctx))
					records := auditLines(t, logs, "http request failed")
					if len(records) != 1 || records[0]["level"] != tc.level || records[0]["err"] != tc.cause.Error() {
						t.Fatalf("diagnostic = %s, want %s with cause", logs, tc.level)
					}
					msg := "http"
					if method == http.MethodPost {
						msg = "audit"
					}
					status := auditLines(t, logs, msg)
					wantStatus := tc.wantStatus
					if out.Code != wantStatus || len(status) != 1 || status[0]["status"] != float64(out.Code) || status[0]["level"] != "INFO" || status[0]["request_id"] != out.Header().Get(RequestIDHeader) {
						t.Fatalf("HTTP %d, status/audit = %s", out.Code, logs)
					}
				})
			}
		}
	}
}

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
		req := httptest.NewRequest(http.MethodPut, signed.URL, failingUploadBody{errors.New("upload read: " + secret)}).WithContext(ctx)
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

func TestRequestCancellationPreservesCommittedStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		commit func(http.ResponseWriter)
		want   int
		cause  error
	}{
		{"explicit", func(w http.ResponseWriter) { w.WriteHeader(http.StatusAccepted) }, http.StatusAccepted, context.Canceled},
		{"body", func(w http.ResponseWriter) { _, _ = w.Write([]byte("started")) }, http.StatusOK, context.Canceled},
		{"flush", func(w http.ResponseWriter) { w.(http.Flusher).Flush() }, http.StatusOK, context.Canceled},
		{"informational", func(w http.ResponseWriter) { w.WriteHeader(http.StatusEarlyHints) }, statusClientClosedRequest, context.Canceled},
		{"switching protocols", func(w http.ResponseWriter) { w.WriteHeader(http.StatusSwitchingProtocols) }, http.StatusSwitchingProtocols, context.Canceled},
		{"bad connection", func(w http.ResponseWriter) { w.WriteHeader(http.StatusAccepted) }, http.StatusAccepted, driver.ErrBadConn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			h, logs := auditHarness(t, "/api/v1/runs", func(w http.ResponseWriter, _ *http.Request) {
				tc.commit(w)
				cancel()
				writeError(w, http.StatusInternalServerError, tc.cause)
			})
			wire := &committedStatusWriter{header: make(http.Header)}
			h.ServeHTTP(wire, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil).WithContext(ctx))
			records := auditLines(t, logs, "http")
			if wire.status != tc.want || len(records) != 1 || records[0]["status"] != float64(tc.want) {
				t.Fatalf("wire=%d want=%d logs=%s", wire.status, tc.want, logs)
			}
		})
	}
}

type committedStatusWriter struct {
	header http.Header
	status int
}

func (w *committedStatusWriter) Header() http.Header { return w.header }

func (w *committedStatusWriter) WriteHeader(code int) {
	if w.status == 0 && (code < 100 || code >= 200 || code == http.StatusSwitchingProtocols) {
		w.status = code
	}
}

func (w *committedStatusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return len(body), nil
}

func (w *committedStatusWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
}

func TestRequestCancellationPreservesClientRefusal(t *testing.T) {
	for _, writer := range []string{"policy", "legacy"} {
		t.Run(writer, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			h, logs := auditHarness(t, "/api/v1/runs/{run}/local-host/snapshot", func(w http.ResponseWriter, _ *http.Request) {
				cancel()
				cause := errors.Join(&apierr.Error{Code: apierr.NotFound, Message: "resource not found"}, driver.ErrBadConn)
				if writer == "policy" {
					routepolicy.WritePolicyError(w, cause)
				} else {
					writeError(w, http.StatusNotFound, cause)
				}
			})
			out := httptest.NewRecorder()
			h.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run/local-host/snapshot", nil).WithContext(ctx))
			if out.Code != http.StatusNotFound || len(auditLines(t, logs, "http request failed")) != 0 {
				t.Fatalf("refusal HTTP %d: %s logs=%s", out.Code, out.Body, logs)
			}
		})
	}
}
