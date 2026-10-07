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
	logsURL        string
	logSeal        childLogSeal
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
		b.logsURL = logsURL
		b.logSeal.init()
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
	logsRequest := strings.HasPrefix(r.URL.Path, "/api/v1/logs/")
	if !b.allow(r) || (logsRequest && b.logs == nil) {
		http.Error(w, "execution capability does not allow this route", http.StatusForbidden)
		return
	}
	if strings.HasPrefix(r.URL.EscapedPath(), "/bin/") {
		b.serveArtifact(w, r)
		return
	}
	if err := b.bindRequest(r); err != nil {
		http.Error(w, "invalid execution request", http.StatusBadRequest)
		return
	}
	if logsRequest {
		r.Host = b.logsHost
		if b.isChildLogAppend(r) {
			b.forwardChildLogAppend(w, r)
			return
		}
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

// safety: {run} and {node} match only this broker's own run and node, {other} any node of
// this run, {name} any one segment, and a trailing {name...} the rest of the path.
type brokerRoute struct {
	method, pattern string
	check           func(b *remoteExecutionBroker, r *http.Request, segments map[string]string) bool
}

// safety: this is everything a brokered node reaches; docs/node-protocol.md lists the
// same set, and a test holds the two equal.
var brokerRoutes = []brokerRoute{
	{http.MethodGet, "/api/v1/runs/{run}", nil},
	{http.MethodGet, "/api/v1/triggers/{run}", nil},
	{http.MethodGet, "/api/v1/runs/{run}/steps", nil},
	{http.MethodGet, "/api/v1/runs/{run}/nodes/{other}", nil},
	{http.MethodGet, "/api/v1/runs/{run}/nodes/{other}/output", nil},
	{http.MethodGet, "/api/v1/runs/{run}/nodes/{node}/bounce", nil},
	{http.MethodGet, "/api/v1/secrets/{name}", secretForThisRun},
	{http.MethodPost, "/api/v1/runs/{run}/events", nil},
	{http.MethodPost, "/api/v1/runs/{run}/heartbeat", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/start", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/finish", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/deps", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/dispatch", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/metrics", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/execution-start", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/execution-finish", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/output-upload", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/output-commit", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/activity", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/touch", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/annotations", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/summary", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/artifact-manifest", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/steps/start", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/steps/finish", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/steps/skip", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/steps/annotations", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/steps/summary", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/bounce/consume", nil},
	{http.MethodPost, "/api/v1/runs/{run}/nodes/{node}/status", nil},
	{http.MethodGet, "/api/v1/concurrency/{key}/holder", concurrencyForThisNode},
	{http.MethodGet, "/api/v1/concurrency/{key}/resolve", concurrencyForThisNode},
	{http.MethodPost, "/api/v1/concurrency/{key}/acquire", concurrencyForThisNode},
	{http.MethodPost, "/api/v1/concurrency/{key}/heartbeat", concurrencyForThisNode},
	{http.MethodPost, "/api/v1/concurrency/{key}/release", concurrencyForThisNode},
	{http.MethodPost, "/api/v1/concurrency/{key}/cancel-waiter", concurrencyForThisNode},
	{http.MethodGet, "/api/v1/logs/{run}/{node}", nil},
	{http.MethodPost, "/api/v1/logs/{run}/{node}", nil},
	{http.MethodGet, "/api/v1/logs/{run}/{node}/seal", nil},
	{http.MethodPost, "/api/v1/logs/{run}/{node}/seal", nil},
	{http.MethodGet, "/api/v1/logs/{run}/{node}/stream", nil},
	{http.MethodGet, "/bin/{key...}", nil},
	{http.MethodHead, "/bin/{key...}", nil},
	{http.MethodPut, "/bin/{key...}", nil},
}

func (b *remoteExecutionBroker) allow(r *http.Request) bool {
	path := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	for _, route := range brokerRoutes {
		if route.method != r.Method {
			continue
		}
		segments, ok := b.matchRoute(route.pattern, path)
		if !ok {
			continue
		}
		return route.check == nil || route.check(b, r, segments)
	}
	return false
}

func (b *remoteExecutionBroker) matchRoute(pattern string, path []string) (map[string]string, bool) {
	want := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	segments := map[string]string{}
	for i, w := range want {
		if strings.HasSuffix(w, "...}") {
			if i >= len(path) {
				return nil, false
			}
			segments[strings.Trim(w, "{.}")] = strings.Join(path[i:], "/")
			return segments, true
		}
		if i >= len(path) {
			return nil, false
		}
		got := path[i]
		switch {
		case w == "{run}":
			if got != url.PathEscape(b.runID) {
				return nil, false
			}
		case w == "{node}":
			if got != url.PathEscape(b.nodeID) {
				return nil, false
			}
		case w == "{other}":
			node, err := url.PathUnescape(got)
			if err != nil || node == "" || node == "." || node == ".." || strings.ContainsAny(node, "/\\") {
				return nil, false
			}
		case strings.HasPrefix(w, "{"):
			segments[strings.Trim(w, "{}")] = got
		case w != got:
			return nil, false
		}
	}
	return segments, len(path) == len(want)
}

func secretForThisRun(b *remoteExecutionBroker, r *http.Request, _ map[string]string) bool {
	return r.URL.Query().Get("run") == b.runID
}

func concurrencyForThisNode(b *remoteExecutionBroker, r *http.Request, segments map[string]string) bool {
	key, err := url.PathUnescape(segments["key"])
	if err != nil || key == "" || strings.Contains(key, "/") {
		return false
	}
	holder := b.runID + "/" + b.nodeID
	action := r.URL.EscapedPath()[strings.LastIndex(r.URL.EscapedPath(), "/")+1:]
	switch action {
	case "holder":
		return r.URL.Query().Get("holder_id") == holder
	case "resolve":
		return r.URL.Query().Get("run_id") == b.runID && r.URL.Query().Get("node_id") == b.nodeID
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		return false
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	var slot struct {
		HolderID          string `json:"holder_id"`
		InheritedHolderID string `json:"inherited_holder_id"`
		RunID             string `json:"run_id"`
		NodeID            string `json:"node_id"`
	}
	if json.Unmarshal(body, &slot) != nil {
		return false
	}
	if action == "cancel-waiter" {
		return slot.RunID == b.runID && slot.NodeID == b.nodeID
	}
	if slot.HolderID != holder {
		return false
	}
	if action == "acquire" {
		return slot.RunID == b.runID && slot.NodeID == b.nodeID &&
			slot.InheritedHolderID == ""
	}
	return true
}

func (b *remoteExecutionBroker) bindRequest(r *http.Request) error {
	// safety: nil prevents the loopback hop from appearing as public ingress.
	r.Header["X-Forwarded-For"] = nil
	r.Header.Del("X-Forwarded-Host")
	r.Header.Del("X-Forwarded-Proto")
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
