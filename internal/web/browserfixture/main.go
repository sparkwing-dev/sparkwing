package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	dashboard "github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const serviceToken = "browser-fixture-service-token"

type controllerState struct {
	sync.Mutex
	created             bool
	loginCalls          int
	sessionHeaders      []string
	logoutSessions      []string
	proxyAuthorizations []string
	activeSessions      map[string]bool
}

type stateSnapshot struct {
	Created             bool     `json:"created"`
	LoginCalls          int      `json:"login_calls"`
	SessionHeaders      []string `json:"session_headers"`
	LogoutSessions      []string `json:"logout_sessions"`
	ProxyAuthorizations []string `json:"proxy_authorizations"`
}

func (s *controllerState) snapshot() stateSnapshot {
	s.Lock()
	defer s.Unlock()
	return stateSnapshot{
		Created:             s.created,
		LoginCalls:          s.loginCalls,
		SessionHeaders:      append([]string(nil), s.sessionHeaders...),
		LogoutSessions:      append([]string(nil), s.logoutSessions...),
		ProxyAuthorizations: append([]string(nil), s.proxyAuthorizations...),
	}
}

func (s *controllerState) handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/__fixture/state" {
		_ = json.NewEncoder(w).Encode(s.snapshot())
		return
	}

	s.Lock()
	defer s.Unlock()
	switch r.URL.Path {
	case "/api/v1/auth/bootstrap-needed":
		_ = json.NewEncoder(w).Encode(map[string]bool{"needed": !s.created})
	case "/api/v1/users":
		var body map[string]string
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil ||
			body["name"] != "admin" || body["password"] != "correct-horse" || s.created {
			http.Error(w, "bad bootstrap request", http.StatusBadRequest)
			return
		}
		s.created = true
		w.WriteHeader(http.StatusCreated)
	case "/api/v1/auth/login":
		var body map[string]string
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil ||
			!s.created || body["username"] != "admin" || body["password"] != "correct-horse" {
			http.Error(w, "bad login request", http.StatusUnauthorized)
			return
		}
		s.loginCalls++
		sessionID := fmt.Sprintf("session-%d", s.loginCalls)
		s.activeSessions[sessionID] = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"session_id": sessionID,
			"csrf_token": "csrf-token",
			"principal":  "admin",
			"scopes":     []string{"admin"},
			"expires_at": time.Now().Add(time.Hour).Unix(),
		})
	case "/api/v1/auth/session":
		header := r.Header.Get("Authorization")
		s.sessionHeaders = append(s.sessionHeaders, header)
		const prefix = "Session "
		if len(header) <= len(prefix) || header[:len(prefix)] != prefix || !s.activeSessions[header[len(prefix):]] {
			http.Error(w, "invalid session", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"principal":  "admin",
			"scopes":     []string{"admin"},
			"csrf_token": "csrf-token",
			"expires_at": time.Now().Add(time.Hour).Unix(),
		})
	case "/api/v1/auth/logout":
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		sessionID := body["session_id"]
		s.logoutSessions = append(s.logoutSessions, sessionID)
		delete(s.activeSessions, sessionID)
		w.WriteHeader(http.StatusNoContent)
	case "/api/v1/runs":
		s.proxyAuthorizations = append(s.proxyAuthorizations, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{"runs": []any{}})
	case "/api/v1/approvals/pending":
		s.proxyAuthorizations = append(s.proxyAuthorizations, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{"approvals": []any{}})
	case "/api/v1/health":
		s.proxyAuthorizations = append(s.proxyAuthorizations, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.NotFound(w, r)
	}
}

func listen(handler http.Handler) (net.Listener, *http.Server, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("browser fixture server: %v", err)
		}
	}()
	return listener, server, nil
}

func main() {
	root := os.Getenv("SPARKWING_BROWSER_FIXTURE_HOME")
	if root == "" {
		log.Fatal("SPARKWING_BROWSER_FIXTURE_HOME is required")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		log.Fatal(err)
	}
	dashboardPaths := paths.PathsAt(filepath.Join(root, "home"))
	if err := dashboardPaths.EnsureRoot(); err != nil {
		log.Fatal(err)
	}
	stateStore, err := store.Open(dashboardPaths.StateDB())
	if err != nil {
		log.Fatal(err)
	}
	defer stateStore.Close()

	state := &controllerState{activeSessions: map[string]bool{}}
	controllerListener, controllerServer, err := listen(http.HandlerFunc(state.handler))
	if err != nil {
		log.Fatal(err)
	}
	controllerOrigin := "http://" + controllerListener.Addr().String()
	webOutput := os.Getenv("SPARKWING_BROWSER_WEB_OUT")
	if webOutput == "" {
		log.Fatal("SPARKWING_BROWSER_WEB_OUT is required")
	}
	dashboardHandler := dashboard.HandlerFromOptionsWithBundle(dashboard.HandlerOptions{
		Backend:       backend.NewStoreBackend(stateStore, dashboardPaths, nil),
		Paths:         dashboardPaths,
		ControllerURL: controllerOrigin,
		APIURL:        controllerOrigin,
		Token:         serviceToken,
		Version:       "auth-browser-fixture",
		RequireLogin:  true,
	}, os.DirFS(webOutput))
	dashboardListener, dashboardServer, err := listen(dashboardHandler)
	if err != nil {
		_ = controllerServer.Close()
		log.Fatal(err)
	}

	started := map[string]string{
		"origin":            "http://" + dashboardListener.Addr().String(),
		"controller_origin": controllerOrigin,
		"service_token":     serviceToken,
	}
	if err := json.NewEncoder(os.Stdout).Encode(started); err != nil {
		log.Fatal(err)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	_ = dashboardServer.Close()
	_ = controllerServer.Close()
}
