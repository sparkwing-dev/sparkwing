package logs_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/logs"
)

func TestAppend_403ReturnsAuthErrorWithScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "token lacks required scope: logs.write", http.StatusForbidden)
	}))
	defer srv.Close()

	c := logs.NewClient(srv.URL, nil)
	err := c.Append(context.Background(), "run-1", "step-a", []byte("hi"))
	if err == nil {
		t.Fatal("expected error from 403")
	}
	var ae *logs.AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
	if ae.Status != http.StatusForbidden {
		t.Errorf("Status: got %d, want 403", ae.Status)
	}
	if ae.Scope != "logs.write" {
		t.Errorf("Scope: got %q, want %q", ae.Scope, "logs.write")
	}
	if !strings.Contains(ae.Error(), "logs.write") {
		t.Errorf("Error() should mention scope: %q", ae.Error())
	}
}

func TestAppend_401ReturnsAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := logs.NewClient(srv.URL, nil)
	err := c.Append(context.Background(), "run-1", "step-a", []byte("hi"))
	var ae *logs.AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
	if ae.Status != http.StatusUnauthorized {
		t.Errorf("Status: got %d, want 401", ae.Status)
	}
	if ae.Error() == "" {
		t.Error("Error() should not be empty")
	}
}

func TestAppend_403JSONBodyExtractsScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(logs.AuthErrorBody{
			Error:        "missing_scope",
			MissingScope: "logs.write",
			Principal:    "runner:warm-runner-7",
			Message:      "your token cannot append logs (logs.write missing)",
		})
	}))
	defer srv.Close()

	c := logs.NewClient(srv.URL, nil)
	err := c.Append(context.Background(), "run-1", "step-a", []byte("hi"))
	var ae *logs.AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
	if ae.Scope != "logs.write" {
		t.Errorf("Scope: got %q, want %q (must come from JSON, not message)", ae.Scope, "logs.write")
	}
}

func TestAppend_403JSONBodyNoScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(logs.AuthErrorBody{
			Error:   "forbidden",
			Message: "denied by policy",
		})
	}))
	defer srv.Close()

	c := logs.NewClient(srv.URL, nil)
	err := c.Append(context.Background(), "run-1", "step-a", []byte("hi"))
	var ae *logs.AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *AuthError, got %T: %v", err, err)
	}
	if ae.Scope != "" {
		t.Errorf("Scope: got %q, want empty", ae.Scope)
	}
	if !strings.Contains(ae.Error(), "denied by policy") {
		t.Errorf("Error() should fall back to RawBody: %q", ae.Error())
	}
}

func TestAppend_5xxNotAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := logs.NewClient(srv.URL, nil)
	err := c.Append(context.Background(), "run-1", "step-a", []byte("hi"))
	if err == nil {
		t.Fatal("expected error from 500")
	}
	var ae *logs.AuthError
	if errors.As(err, &ae) {
		t.Errorf("5xx must not be *AuthError, got: %v", ae)
	}
}
