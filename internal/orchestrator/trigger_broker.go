package orchestrator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// TriggerBroker stands between a claimed trigger's pipeline binary and the
// controller. The binary is the team's own code, so it holds a capability
// that this broker accepts only for the claimed run's routes, and the
// runner's token stays in the runner process.
type TriggerBroker struct {
	server        *http.Server
	listener      net.Listener
	capability    string
	upstreamToken string
	runID         string
	pipeline      string
	controller    *httputil.ReverseProxy
	logs          *httputil.ReverseProxy
	logger        *slog.Logger

	mu       sync.Mutex
	children map[string]bool
}

// StartTriggerBroker serves the controller and logs routes trigger's handler
// needs on a loopback port. Pass [TriggerBroker.URL] as the child's
// controller and logs URL and [TriggerBroker.Capability] as its token.
func StartTriggerBroker(controllerURL, logsURL, upstreamToken string, trigger *store.Trigger, logger *slog.Logger) (*TriggerBroker, error) {
	if logger == nil {
		logger = slog.Default()
	}
	controllerTarget, err := executionBrokerTarget(controllerURL)
	if err != nil {
		return nil, fmt.Errorf("controller target: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("trigger capability: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("trigger broker listen: %w", err)
	}
	b := &TriggerBroker{
		listener: listener, capability: base64.RawURLEncoding.EncodeToString(raw),
		upstreamToken: upstreamToken, runID: trigger.ID, pipeline: trigger.Pipeline,
		controller: brokerProxy(controllerTarget, logger), logger: logger,
		children: map[string]bool{},
	}
	b.logs = b.controller
	if logsURL != "" {
		logsTarget, err := executionBrokerTarget(logsURL)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("logs target: %w", err), listener.Close())
		}
		b.logs = brokerProxy(logsTarget, logger)
	}
	b.controller.ModifyResponse = b.recordChild
	// safety: a trigger handler holds event streams and awaits child runs for
	// as long as its run lasts, so no write deadline cuts a response short.
	b.server = &http.Server{Handler: b, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	go func() {
		if err := b.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("trigger broker stopped", "run_id", b.runID, "err", err)
		}
	}()
	return b, nil
}

func brokerProxy(target *url.URL, logger *slog.Logger) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite:       func(pr *httputil.ProxyRequest) { pr.SetURL(target) },
		FlushInterval: -1,
		ErrorHandler:  executionBrokerProxyError(logger),
	}
}

// URL is the loopback base URL the child reaches the controller and logs at.
func (b *TriggerBroker) URL() string { return "http://" + b.listener.Addr().String() }

// Capability is the bearer the child presents to the broker.
func (b *TriggerBroker) Capability() string { return b.capability }

// Close stops the broker; the capability dies with it.
func (b *TriggerBroker) Close(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := b.server.Shutdown(ctx); err != nil {
		b.logger.Warn("trigger broker shutdown incomplete", "run_id", b.runID, "err", err)
	}
}

func (b *TriggerBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(b.capability)) != 1 {
		// safety: a direct-data or gitcache call carries the run's own cache
		// grant, which the controller checks, so it passes through unchanged.
		if strings.HasPrefix(got, "swcg1.") && b.grantRoute(r) {
			b.controller.ServeHTTP(w, r)
			return
		}
		http.Error(w, "trigger capability required", http.StatusUnauthorized)
		return
	}
	logs := strings.HasPrefix(r.URL.Path, "/api/v1/logs/")
	allowed := b.allowLogs(r)
	if !logs {
		allowed = b.allowController(r)
	}
	if !allowed {
		b.logger.Warn("trigger broker refused a route outside its run",
			"run_id", b.runID, "method", r.Method, "path", r.URL.Path)
		http.Error(w, "trigger capability does not allow this route", http.StatusForbidden)
		return
	}
	r.Header.Set("Authorization", "Bearer "+b.upstreamToken)
	if logs {
		b.logs.ServeHTTP(w, r)
		return
	}
	b.controller.ServeHTTP(w, r)
}

func (b *TriggerBroker) grantRoute(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/v1/data/") || b.underRun(r.URL.EscapedPath(), b.runID)
}

func (b *TriggerBroker) underRun(path, runID string) bool {
	escaped := url.PathEscape(runID)
	for _, prefix := range []string{"/api/v1/runs/", "/api/v1/triggers/"} {
		if path == prefix+escaped || strings.HasPrefix(path, prefix+escaped+"/") {
			return true
		}
	}
	return false
}

func (b *TriggerBroker) allowLogs(r *http.Request) bool {
	prefix := "/api/v1/logs/" + url.PathEscape(b.runID)
	path := r.URL.EscapedPath()
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func (b *TriggerBroker) allowController(r *http.Request) bool {
	path := r.URL.EscapedPath()
	if b.underRun(path, b.runID) {
		return true
	}
	if r.Method == http.MethodGet {
		switch {
		case path == "/api/v1/health" || path == "/api/v1/capabilities" || path == "/api/v1/services":
			return true
		case strings.HasPrefix(path, "/api/v1/secrets/"):
			return r.URL.Query().Get("run") == b.runID
		case path == "/api/v1/triggers/spawned-child":
			return r.URL.Query().Get("parent_run_id") == b.runID
		case strings.HasPrefix(path, "/api/v1/pipelines/") && strings.HasSuffix(path, "/latest"):
			return true
		case strings.HasPrefix(path, "/api/v1/runs/") && strings.Contains(path, "/nodes/") && strings.HasSuffix(path, "/output"):
			return true
		}
		if b.child(path) {
			return true
		}
	}
	if strings.HasPrefix(path, "/api/v1/pipelines/"+url.PathEscape(b.pipeline)+"/") {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/concurrency/") {
		return b.allowConcurrency(r)
	}
	if r.Method != http.MethodPost {
		return false
	}
	switch path {
	case "/api/v1/runs":
		var body struct {
			ID string `json:"id"`
		}
		return peekBody(r, &body) && body.ID == b.runID
	case "/api/v1/triggers":
		var body struct {
			ParentRunID string `json:"parent_run_id"`
		}
		return peekBody(r, &body) && body.ParentRunID == b.runID
	}
	return b.child(path) && strings.HasSuffix(path, "/cancel")
}

// safety: a slot names its run in the query or the body, and a holder id is
// run/node, so the handler reaches only its own run's slots.
func (b *TriggerBroker) allowConcurrency(r *http.Request) bool {
	q := r.URL.Query()
	own := func(runID, holderID string) bool {
		return runID == b.runID || (runID == "" && strings.HasPrefix(holderID, b.runID+"/"))
	}
	if r.Method == http.MethodGet {
		return own(q.Get("run_id"), q.Get("holder_id"))
	}
	var body struct {
		RunID    string `json:"run_id"`
		HolderID string `json:"holder_id"`
	}
	return r.Method == http.MethodPost && peekBody(r, &body) &&
		own(body.RunID, body.HolderID) && (body.HolderID == "" || strings.HasPrefix(body.HolderID, b.runID+"/"))
}

func (b *TriggerBroker) child(path string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range b.children {
		if b.underRun(path, id) {
			return true
		}
	}
	return false
}

func (b *TriggerBroker) recordChild(resp *http.Response) error {
	if resp.Request.Method != http.MethodPost || resp.Request.URL.Path != "/api/v1/triggers" ||
		resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	var created struct {
		RunID string `json:"run_id"`
	}
	if json.Unmarshal(raw, &created) == nil && created.RunID != "" {
		b.mu.Lock()
		b.children[created.RunID] = true
		b.mu.Unlock()
	}
	return nil
}

func peekBody(r *http.Request, into any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return err == nil && len(raw) <= 1<<20 && json.Unmarshal(raw, into) == nil
}
