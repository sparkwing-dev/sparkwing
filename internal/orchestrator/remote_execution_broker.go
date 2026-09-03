package orchestrator

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

const (
	remoteExecutionCapabilityEnv = "SPARKWING_EXECUTION_CAPABILITY"
	remoteBrokeredClaimEnv       = "SPARKWING_BROKERED_NODE_CLAIM"
)

type remoteExecutionBroker struct {
	server         *http.Server
	listener       net.Listener
	capability     string
	upstreamToken  string
	runID          string
	nodeID         string
	fence          store.NodeClaimFence
	controller     *httputil.ReverseProxy
	logs           *httputil.ReverseProxy
	artifact       storage.ArtifactStore
	controllerHost string
	logsHost       string
}

func startRemoteExecutionBroker(
	controllerURL, logsURL, upstreamToken, runID, nodeID string,
	fence store.NodeClaimFence,
	artifact storage.ArtifactStore,
	logger *slog.Logger,
) (*remoteExecutionBroker, error) {
	controllerTarget, err := executionBrokerTarget(controllerURL)
	if err != nil {
		return nil, fmt.Errorf("controller target: %w", err)
	}
	var logsTarget *url.URL
	if logsURL != "" {
		logsTarget, err = executionBrokerTarget(logsURL)
		if err != nil {
			return nil, fmt.Errorf("logs target: %w", err)
		}
	}
	capabilityBytes := make([]byte, 32)
	if _, err := rand.Read(capabilityBytes); err != nil {
		return nil, fmt.Errorf("execution capability: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("execution broker listen: %w", err)
	}
	b := &remoteExecutionBroker{
		listener: listener, capability: base64.RawURLEncoding.EncodeToString(capabilityBytes),
		upstreamToken: upstreamToken, runID: runID, nodeID: nodeID, fence: fence,
		artifact:   artifact,
		controller: httputil.NewSingleHostReverseProxy(controllerTarget), controllerHost: controllerTarget.Host,
	}
	if logsTarget != nil {
		b.logs = httputil.NewSingleHostReverseProxy(logsTarget)
		b.logsHost = logsTarget.Host
	}
	b.controller.ErrorHandler = executionBrokerProxyError(logger)
	if b.logs != nil {
		b.logs.ErrorHandler = executionBrokerProxyError(logger)
	}
	b.server = &http.Server{
		Handler:           b,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      65 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		if err := b.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && logger != nil {
			logger.Warn("remote execution broker stopped", "err", err)
		}
	}()
	return b, nil
}

func executionBrokerTarget(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil || target.Scheme == "" || target.Host == "" || target.User != nil ||
		(target.Scheme != "http" && target.Scheme != "https") {
		return nil, errors.New("absolute http(s) URL without userinfo required")
	}
	return target, nil
}

func executionBrokerProxyError(logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		if logger != nil {
			logger.Warn("remote execution broker upstream failed", "method", r.Method, "path", r.URL.Path, "err", err)
		}
		http.Error(w, "execution upstream unavailable", http.StatusBadGateway)
	}
}

func (b *remoteExecutionBroker) URL() string {
	return "http://" + b.listener.Addr().String()
}

func (b *remoteExecutionBroker) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = b.server.Shutdown(ctx)
}

func (b *remoteExecutionBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(got) != len(b.capability) || subtle.ConstantTimeCompare([]byte(got), []byte(b.capability)) != 1 {
		http.Error(w, "execution capability required", http.StatusUnauthorized)
		return
	}
	if strings.HasPrefix(r.URL.EscapedPath(), "/bin/") {
		b.serveArtifact(w, r)
		return
	}
	logsRequest := strings.HasPrefix(r.URL.Path, "/api/v1/logs/")
	if logsRequest {
		if b.logs == nil || !b.allowLogs(r) {
			http.Error(w, "execution capability does not allow this route", http.StatusForbidden)
			return
		}
	} else if !b.allowController(r) {
		http.Error(w, "execution capability does not allow this route", http.StatusForbidden)
		return
	}
	if err := b.bindRequest(r); err != nil {
		http.Error(w, "invalid execution request", http.StatusBadRequest)
		return
	}
	if logsRequest {
		r.Host = b.logsHost
		b.logs.ServeHTTP(w, r)
		return
	}
	r.Host = b.controllerHost
	b.controller.ServeHTTP(w, r)
}

func (b *remoteExecutionBroker) serveArtifact(w http.ResponseWriter, r *http.Request) {
	if b.artifact == nil {
		http.Error(w, "execution capability does not allow this route", http.StatusForbidden)
		return
	}
	key, err := executionArtifactKey(r.URL.EscapedPath())
	if err != nil {
		http.Error(w, "invalid artifact key", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		body, err := b.artifact.Get(r.Context(), key)
		if errors.Is(err, storage.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "artifact read failed", http.StatusBadGateway)
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, body)
	case http.MethodHead:
		has, err := b.artifact.Has(r.Context(), key)
		if err != nil {
			http.Error(w, "artifact lookup failed", http.StatusBadGateway)
			return
		}
		if !has {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodPut:
		if err := b.putArtifact(r, key); err != nil {
			if errors.Is(err, errArtifactDigestMismatch) {
				http.Error(w, "artifact digest does not match key", http.StatusBadRequest)
				return
			}
			http.Error(w, "artifact write failed", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)
	default:
		http.Error(w, "execution capability does not allow this route", http.StatusForbidden)
	}
}

var errArtifactDigestMismatch = errors.New("artifact digest mismatch")

func executionArtifactKey(path string) (string, error) {
	escaped := strings.TrimPrefix(path, "/bin/")
	parts := strings.Split(escaped, "/")
	for i, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || strings.Contains(decoded, "/") {
			return "", errors.New("invalid encoded artifact segment")
		}
		parts[i] = decoded
	}
	key := strings.Join(parts, "/")
	if err := storage.SafeArtifactKey(key); err != nil {
		return "", err
	}
	if len(parts) != 3 || parts[0] != "artifacts" || (parts[1] != "blobs" && parts[1] != "manifests") || len(parts[2]) != sha256.Size*2 {
		return "", errors.New("artifact key is outside the execution namespace")
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return "", errors.New("artifact key has an invalid digest")
	}
	return key, nil
}

func (b *remoteExecutionBroker) putArtifact(r *http.Request, key string) error {
	tmp, err := os.CreateTemp("", "sparkwing-execution-artifact-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	defer tmp.Close()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), r.Body); err != nil {
		return err
	}
	if !strings.HasSuffix(key, "/"+hex.EncodeToString(hash.Sum(nil))) {
		return errArtifactDigestMismatch
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return b.artifact.Put(r.Context(), key, tmp)
}

func (b *remoteExecutionBroker) allowLogs(r *http.Request) bool {
	prefix := "/api/v1/logs/" + url.PathEscape(b.runID) + "/" + url.PathEscape(b.nodeID)
	return (r.Method == http.MethodGet || r.Method == http.MethodPost) &&
		(r.URL.EscapedPath() == prefix || strings.HasPrefix(r.URL.EscapedPath(), prefix+"/"))
}

func (b *remoteExecutionBroker) allowController(r *http.Request) bool {
	runPath := "/api/v1/runs/" + url.PathEscape(b.runID)
	nodePath := runPath + "/nodes/" + url.PathEscape(b.nodeID)
	triggerPath := "/api/v1/triggers/" + url.PathEscape(b.runID)
	path := r.URL.EscapedPath()
	if r.Method == http.MethodGet {
		if path == runPath || path == triggerPath || path == nodePath || path == nodePath+"/output" || path == nodePath+"/bounce" || path == runPath+"/steps" {
			return true
		}
		if strings.HasPrefix(path, runPath+"/nodes/") && strings.HasSuffix(path, "/output") {
			return true
		}
		if strings.HasPrefix(path, "/api/v1/secrets/") {
			return r.URL.Query().Get("run") == b.runID
		}
		return false
	}
	if r.Method != http.MethodPost {
		return false
	}
	if path == runPath+"/events" || path == runPath+"/heartbeat" {
		return true
	}
	for _, suffix := range []string{
		"/start", "/finish", "/deps", "/dispatch", "/metrics", "/execution-start", "/execution-finish",
		"/activity", "/touch", "/annotations", "/summary", "/artifact-manifest", "/steps/start",
		"/steps/finish", "/steps/skip", "/steps/annotations", "/steps/summary", "/bounce/consume", "/status",
	} {
		if path == nodePath+suffix {
			return true
		}
	}
	return false
}

func (b *remoteExecutionBroker) bindRequest(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+b.upstreamToken)
	for _, header := range []string{
		store.ClaimHolderHeader, store.ClaimMembershipHeader, store.ClaimReservationHeader,
		store.ClaimGenerationHeader, store.TriggerGenerationHeader,
	} {
		r.Header.Del(header)
	}
	r.Header.Set(store.ClaimHolderHeader, b.fence.HolderID)
	r.Header.Set(store.ClaimMembershipHeader, b.fence.MembershipID)
	r.Header.Set(store.ClaimReservationHeader, b.fence.ReservationID)
	r.Header.Set(store.ClaimGenerationHeader, fmt.Sprint(b.fence.ClaimGeneration))
	if strings.HasSuffix(r.URL.Path, "/execution-start") || strings.HasSuffix(r.URL.Path, "/execution-finish") {
		return b.rewriteAttemptBody(r)
	}
	return nil
}

func (b *remoteExecutionBroker) rewriteAttemptBody(r *http.Request) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	value["holder_id"] = b.fence.HolderID
	value["membership_id"] = b.fence.MembershipID
	value["reservation_id"] = b.fence.ReservationID
	value["claim_generation"] = b.fence.ClaimGeneration
	body, err = json.Marshal(value)
	if err != nil {
		return err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", fmt.Sprint(len(body)))
	return nil
}
