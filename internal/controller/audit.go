package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
)

// RequestIDHeader carries a request's correlation id. A caller's value is
// kept when it is a short safe correlation value and replaced otherwise. Every response
// echoes the one the request was logged under.
const RequestIDHeader = "X-Request-Id"

const (
	maxRequestIDLen = 64
	maxAuditValue   = 128
)

// safety: only these path wildcards, and the identity targets below, reach the
// audit record. Other wildcards can carry values a log must not hold.
var auditPathIDs = map[string]string{
	"job":  "job_id",
	"run":  "run_id",
	"team": "target_team",
}

type auditTarget struct {
	key  string
	keep func(string) string
}

var (
	invitationTarget = auditTarget{"invitation_id", opaqueID}
	tokenTarget      = auditTarget{"token_prefix", tokenPrefixTarget}
)

// safety: invitation and metering paths keep only shaped public identifiers.
// Revocation targets come from successful store mutations instead of paths.
var auditTargetIDs = map[string]map[string]auditTarget{
	"/api/v1/team/invitations/{id}":             {"id": invitationTarget},
	"/api/v1/invitations/{id}/accept":           {"id": invitationTarget},
	"/api/v1/operator/runners/{runner}/metered": {"runner": tokenTarget},
}

func opaqueID(v string) string {
	if v == "" || len(v) > 64 || strings.Trim(v, "0123456789abcdef") != "" {
		return ""
	}
	return v
}

func tokenPrefixTarget(v string) string {
	if len(v) < 16 || !strings.HasPrefix(v, capability.ControllerPrefix) || strings.Trim(v[4:16], "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
		return ""
	}
	return v[:16]
}

// safety: a read of these routes can hand out a secret, a credential or every
// team's records, so it is audited with the caller like a write.
var auditedReadPrefixes = []string{
	"/api/v1/secrets", "/api/v1/tokens", "/api/v1/team/tokens", "/api/v1/operator/",
}

// safety: authentication runs below the log middleware, so it fills this
// holder and the line written after the handler returns can name the caller.
type auditRecord struct {
	id, route               string
	path                    string
	principalID, kind, team string
	targets                 []any
	// safety: a GET that changes who can act, such as a sign-in callback, is audited like a write.
	write     bool
	emptyPoll bool
}

type auditCtxKey struct{}

func withAuditRecord(ctx context.Context, id, route string) (context.Context, *auditRecord) {
	rec := &auditRecord{id: id, route: route}
	return context.WithValue(ctx, auditCtxKey{}, rec), rec
}

func notePolicyPrincipal(ctx context.Context, p capability.Principal) {
	observeRequestPrincipal(string(p.Capability.Subject.Kind))
	if rec, ok := ctx.Value(auditCtxKey{}).(*auditRecord); ok {
		rec.principalID, rec.kind, rec.team = p.Capability.Subject.ID, string(p.Capability.Subject.Kind), p.Capability.Team
	}
	otelutil.StampSpan(ctx, otelutil.SpanAttrs{Principal: clip(p.Capability.Subject.ID)})
}

// safety: a browser flow finishes on a GET the provider redirects to, so the step that signs an account in or
// binds an identity or installation marks its request for the audit record a write gets.
func noteAuditWrite(ctx context.Context) {
	if rec, ok := ctx.Value(auditCtxKey{}).(*auditRecord); ok {
		rec.write = true
	}
}

// safety: mutation handlers record revocation targets only after the store commits; the writer sanitizes every field.
func noteAuditTarget(ctx context.Context, key, value string) {
	if rec, ok := ctx.Value(auditCtxKey{}).(*auditRecord); ok && value != "" {
		rec.targets = append(rec.targets, key, value)
	}
}

// safety: another log line names its request by id and route pattern,
// never by the raw path, which can name a secret.
func requestLogAttrs(r *http.Request) []any {
	rec, ok := r.Context().Value(auditCtxKey{}).(*auditRecord)
	if !ok {
		return safeAuditAttrs([]any{"method", r.Method, "route", otherLabel})
	}
	return safeAuditAttrs([]any{"request_id", rec.id, "method", r.Method, "route", rec.route})
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
	if id == "" || len(id) > maxRequestIDLen || pathCredential.MatchString(id) {
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

// safety: unmatched 404s carry only the sanitized path; queries and credentials never reach either line.
func logRequest(ctx context.Context, logger *slog.Logger, r *http.Request, rec *auditRecord,
	status int, elapsed time.Duration, clientIP string,
) {
	if rec.emptyPoll {
		return
	}
	denied := status == http.StatusUnauthorized || status == http.StatusForbidden
	var pathAttrs []any
	if rec.path != "" {
		pathAttrs = []any{"path", rec.path}
	}
	if readOnlyMethod(r.Method) && !denied && !rec.write && !auditedRead(rec.route) {
		attrs := []any{"request_id", rec.id, "method", r.Method, "route", rec.route, "status", status, "dur_ms", elapsed.Milliseconds()}
		logger.InfoContext(ctx, "http", safeAuditAttrs(append(attrs, pathAttrs...))...)
		return
	}
	attrs := []any{
		"ts", time.Now().UTC().Format(time.RFC3339Nano), "request_id", rec.id,
		"method", r.Method, "route", rec.route, "status", status, "dur_ms", elapsed.Milliseconds(),
		"client_ip", clientIP, "client_class", clientClass(r.UserAgent()),
	}
	if rec.kind != "" {
		attrs = append(attrs, "principal_kind", rec.kind, "principal_id", rec.principalID, "team", rec.team)
	}
	attrs = append(attrs, auditPathAttrs(rec.route, r.URL.EscapedPath())...)
	attrs = append(attrs, rec.targets...)
	attrs = append(attrs, pathAttrs...)
	logger.InfoContext(ctx, "audit", safeAuditAttrs(attrs)...)
}

// safety: formatting happens before clipping so a long field cannot expose a truncated credential.
func safeAuditAttrs(attrs []any) []any {
	for i, attr := range attrs {
		if value, ok := attr.(string); ok {
			attrs[i] = formatAuditValue(value)
		}
	}
	return attrs
}

func formatAuditValue(value string) string {
	return clip(pathCredential.ReplaceAllStringFunc(value, func(credential string) string {
		// A stored capability prefix is a public identifier, not a bearer.
		if len(credential) == 16 {
			return credential
		}
		digest := sha256.Sum256([]byte(credential))
		return "[credential:" + hex.EncodeToString(digest[:8]) + "]"
	}))
}

var pathCredential = regexp.MustCompile(`(?:` + strings.Join(capability.CredentialPrefixes(), "|") + `)[A-Za-z0-9_-]+`)

func sanitizedMissingPath(path string, mask func(string) string) string {
	parts := strings.Split(mask(path), "/")
	previous := ""
	for i, part := range parts {
		value, err := url.PathUnescape(part)
		if err != nil {
			value = "[redacted]"
		}
		switch previous {
		case "secrets", "tokens", "sessions":
			value = "[redacted]"
		}
		previous = value
		parts[i] = url.PathEscape(pathCredential.ReplaceAllString(mask(value), "[redacted]"))
	}
	return strings.Join(parts, "/")
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
		keep := func(value string) string { return value }
		if target, ok := auditTargetIDs[route][name]; ok {
			key, keep = target.key, target.keep
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
			if kept := keep(v); kept != "" {
				out = append(out, key, kept)
			}
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
