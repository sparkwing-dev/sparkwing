package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RequestIDHeader carries a request's correlation id. A caller's value is
// kept when it is a short token and replaced otherwise, and every response
// echoes the one the request was logged under.
const RequestIDHeader = "X-Request-Id"

const (
	maxRequestIDLen = 64
	maxAuditValue   = 128
)

// safety: a claim poll that found no work is almost all write traffic and
// changes nothing; the route counters already count it.
var auditSkippedEmptyPolls = map[string]bool{
	"/api/v1/nodes/claim":         true,
	"/api/v1/nodes/claim/prepare": true,
	"/api/v1/triggers/claim":      true,
}

// safety: only these path wildcards reach the audit record. Others name
// secrets, users, invitation ids and similar values a log must not hold.
var auditPathIDs = map[string]string{
	"nodeID": "node_id",
	"team":   "target_team",
}

// safety: authentication runs below the log middleware, so it fills this
// holder and the line written after the handler returns can name the caller.
type auditRecord struct {
	principal *Principal
}

type auditCtxKey struct{}

func withAuditRecord(ctx context.Context) (context.Context, *auditRecord) {
	rec := &auditRecord{}
	return context.WithValue(ctx, auditCtxKey{}, rec), rec
}

// safety: the first principal is the one authentication resolved; a later
// one is a partial copy a limiter builds to key its budget.
func noteAuditPrincipal(ctx context.Context, p *Principal) {
	if rec, ok := ctx.Value(auditCtxKey{}).(*auditRecord); ok && rec.principal == nil {
		rec.principal = p
	}
}

func requestID(r *http.Request) string {
	if id := r.Header.Get(RequestIDHeader); validRequestID(id) {
		return id
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func readOnlyMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// safety: neither line carries the raw path, query, headers, body or
// credential, because a path can name a secret.
func logRequest(ctx context.Context, logger *slog.Logger, r *http.Request, rec *auditRecord,
	id, route string, status int, elapsed time.Duration, clientIP string,
) {
	if readOnlyMethod(r.Method) {
		logger.InfoContext(ctx, "http", "request_id", id, "method", r.Method, "route", route,
			"status", status, "dur_ms", elapsed.Milliseconds())
		return
	}
	if status == http.StatusNoContent && auditSkippedEmptyPolls[route] {
		return
	}
	attrs := []any{
		"ts", time.Now().UTC().Format(time.RFC3339Nano), "request_id", id,
		"method", r.Method, "route", route, "status", status, "dur_ms", elapsed.Milliseconds(),
		"client_ip", clientIP, "user_agent", clip(r.UserAgent()),
	}
	if p := rec.principal; p != nil {
		principalID := p.Name
		if principalID == "" {
			principalID = p.TokenPrefix
		}
		attrs = append(attrs, "principal_kind", p.Kind, "principal_id", clip(principalID), "team", string(p.Team))
	}
	attrs = append(attrs, auditPathAttrs(route, r.URL.EscapedPath())...)
	logger.InfoContext(ctx, "audit", attrs...)
}

func auditPathAttrs(route, path string) []any {
	pattern := strings.Split(strings.Trim(route, "/"), "/")
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < len(pattern) {
		return nil
	}
	var out []any
	for i, part := range pattern {
		name, ok := strings.CutPrefix(part, "{")
		if !ok {
			continue
		}
		name = strings.TrimSuffix(name, "}")
		key := auditPathIDs[name]
		if name == "id" && i == 3 && (pattern[2] == "runs" || pattern[2] == "triggers") {
			key = "run_id"
		}
		if key == "" {
			continue
		}
		if v, err := url.PathUnescape(segments[i]); err == nil {
			out = append(out, key, clip(v))
		}
	}
	return out
}

func clip(s string) string {
	if len(s) <= maxAuditValue {
		return s
	}
	return strings.ToValidUTF8(s[:maxAuditValue], "")
}
