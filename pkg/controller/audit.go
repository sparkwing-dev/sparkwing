package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
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
// changes nothing; the route counters already count it. An executor offer
// on the node claim route also answers 204 while it records a pending offer,
// so that answer is audited.
var auditSkippedEmptyPolls = map[string]bool{
	"/api/v1/nodes/claim":    true,
	"/api/v1/triggers/claim": true,
}

const claimOfferStateHeader = "X-Sparkwing-Claim-Offer-State"

// safety: only these path wildcards, and the identity targets below, reach the
// audit record. Other wildcards can carry values a log must not hold.
var auditPathIDs = map[string]string{
	"nodeID": "node_id",
	"team":   "target_team",
}

// safety: an identity route names its target, an opaque id, a token prefix or
// a secret name, so an investigation can follow one member, invitation,
// token or secret; other routes' wildcards stay out under auditPathIDs.
var auditTargetIDs = map[string]map[string]string{
	"/api/v1/team/members/{user_id}":                      {"user_id": "member_id"},
	"/api/v1/team/invitations/{id}":                       {"id": "invitation_id"},
	"/api/v1/invitations/{id}/accept":                     {"id": "invitation_id"},
	"/api/v1/tokens/{prefix}":                             {"prefix": "token_prefix"},
	"/api/v1/tokens/{prefix}/rotate":                      {"prefix": "token_prefix"},
	"/api/v1/tokens/{prefix}/metered":                     {"prefix": "token_prefix"},
	"/api/v1/team/runner-tokens/{prefix}":                 {"prefix": "token_prefix"},
	"/api/v1/team/runner-tokens/{prefix}/git-credentials": {"prefix": "token_prefix"},
	"/api/v1/team/cli-tokens/{prefix}":                    {"prefix": "token_prefix"},
	"/api/v1/secrets/{name}":                              {"name": "secret_name"},
}

// safety: a read of these routes can hand out a secret, a credential or every
// team's records, so it is audited with the caller like a write.
var auditedReadPrefixes = []string{
	"/api/v1/secrets", "/api/v1/tokens", "/api/v1/team/runner-tokens", "/api/v1/team/cli-tokens", "/api/v1/operator/",
}

// safety: authentication runs below the log middleware, so it fills this
// holder and the line written after the handler returns can name the caller.
type auditRecord struct {
	id, route string
	principal *Principal
	targets   []any
}

type auditCtxKey struct{}

func withAuditRecord(ctx context.Context, id, route string) (context.Context, *auditRecord) {
	rec := &auditRecord{id: id, route: route}
	return context.WithValue(ctx, auditCtxKey{}, rec), rec
}

func noteAuditPrincipal(ctx context.Context, p *Principal) {
	if rec, ok := ctx.Value(auditCtxKey{}).(*auditRecord); ok {
		rec.principal = p
	}
}

// safety: callers pass identifiers only, an opaque id, a role, a token prefix or a secret name, never a value or email.
func noteAuditTarget(ctx context.Context, key, value string) {
	if rec, ok := ctx.Value(auditCtxKey{}).(*auditRecord); ok && value != "" {
		rec.targets = append(rec.targets, key, clip(value))
	}
}

// safety: another log line names its request by id and route pattern,
// never by the raw path, which can name a secret.
func requestLogAttrs(r *http.Request) []any {
	rec, ok := r.Context().Value(auditCtxKey{}).(*auditRecord)
	if !ok {
		return []any{"method", r.Method, "route", otherLabel}
	}
	return []any{"request_id", rec.id, "method", r.Method, "route", rec.route}
}

// safety: a signed-in principal's name is its email address, so the record
// names the account by its opaque id and a token by its prefix.
func auditPrincipalID(p *Principal) string {
	switch {
	case p.AccountID != "":
		return p.AccountID
	case p.TokenPrefix != "":
		return p.TokenPrefix
	case strings.Contains(p.Name, "@"):
		return ""
	default:
		return clip(p.Name)
	}
}

func stampPrincipal(ctx context.Context, p *Principal) {
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Principal: auditPrincipalID(p)})
}

var sparkwingAgent = regexp.MustCompile(`^(sparkwing-[a-z]{1,24})(?:/(v?[0-9][0-9A-Za-z.+-]{0,31}))?(?:[ ;(]|$)`)

// safety: a user agent is whatever the caller sends, including an address
// or a token, so the record keeps only a class read from it: a Sparkwing
// component and its version, a browser, a Go client, or other.
func clientClass(ua string) string {
	if m := sparkwingAgent.FindStringSubmatch(ua); m != nil {
		if m[2] == "" {
			return m[1]
		}
		return m[1] + "/" + m[2]
	}
	switch {
	case strings.HasPrefix(ua, "Mozilla/"):
		return "browser"
	case strings.HasPrefix(ua, "Go-http-client/"):
		return "go-http-client"
	default:
		return "other"
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
	answer http.Header, status int, elapsed time.Duration, clientIP string,
) {
	denied := status == http.StatusUnauthorized || status == http.StatusForbidden
	if readOnlyMethod(r.Method) && !denied && !auditedRead(rec.route) {
		logger.InfoContext(ctx, "http", "request_id", rec.id, "method", r.Method, "route", rec.route,
			"status", status, "dur_ms", elapsed.Milliseconds())
		return
	}
	if status == http.StatusNoContent && auditSkippedEmptyPolls[rec.route] && answer.Get(claimOfferStateHeader) != "pending" {
		return
	}
	attrs := []any{
		"ts", time.Now().UTC().Format(time.RFC3339Nano), "request_id", rec.id,
		"method", r.Method, "route", rec.route, "status", status, "dur_ms", elapsed.Milliseconds(),
		"client_ip", clientIP, "client_class", clientClass(r.UserAgent()),
	}
	if p := rec.principal; p != nil {
		attrs = append(attrs, "principal_kind", p.Kind, "principal_id", auditPrincipalID(p), "team", string(p.Team))
	}
	attrs = append(attrs, auditPathAttrs(rec.route, r.URL.EscapedPath())...)
	attrs = append(attrs, rec.targets...)
	logger.InfoContext(ctx, "audit", attrs...)
}

func auditedRead(route string) bool {
	for _, prefix := range auditedReadPrefixes {
		if strings.HasPrefix(route, prefix) {
			return true
		}
	}
	return false
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
		if target, ok := auditTargetIDs[route][name]; ok {
			key = target
		}
		if name == "id" && i == 3 && (pattern[2] == "runs" || pattern[2] == "triggers") {
			key = "run_id"
		}
		// safety: a repository is named only under repos/{owner}/{name}; a
		// {name} anywhere else can name a secret.
		if name == "owner" && pattern[i-1] == "repos" {
			key = "repo_owner"
		}
		if name == "name" && i > 1 && pattern[i-2] == "repos" && pattern[i-1] == "{owner}" {
			key = "repo_name"
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
