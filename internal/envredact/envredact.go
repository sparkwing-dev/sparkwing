// Package envredact classifies environment variables that must not be
// persisted verbatim. It matches on the name, on the value shape, and
// rewrites URL and DSN userinfo so a stored environment keeps the host
// without the password.
package envredact

import (
	"encoding/json"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

var credentialSubstrings = []string{
	"TOKEN",
	"SECRET",
	"PASSWORD",
	"PASS",
	"KEY",
	"CREDENTIAL",
	"DSN",
	"AUTH",
	"PRIVATE",
	"PEM",
	"CERT",
}

var credentialExact = map[string]bool{
	"SPARKWING_AGENT_TOKEN": true,
	"SPARKWING_CACHE_TOKEN": true,
	"SPARKWING_LEASE_TOKEN": true,
	"SPARKWING_SECRETS_KEY": true,
	"SPARKWING_PG_URL":      true,
	"SPARKWING_TEST_PG_URL": true,
	"GITHUB_TOKEN":          true,
	"DATABASE_URL":          true,
	"PGPASSWORD":            true,
	"PGURL":                 true,
}

var nonCredentialExact = map[string]bool{
	"GIT_AUTHOR_NAME":                       true,
	"GIT_AUTHOR_EMAIL":                      true,
	"GIT_AUTHOR_DATE":                       true,
	"GOPRIVATE":                             true,
	"SSH_KEY_DIR":                           true,
	"DOCKER_TLS_CERTDIR":                    true,
	"SPARKWING_REQUIRE_AUTH":                true,
	"SPARKWING_CACHE_ALLOW_UNAUTHENTICATED": true,
}

var jsonCredentialFields = []string{"TOKEN", "PASSWORD"}

const jsonScanDepth = 8

// CredentialName reports whether an environment variable name is
// credential-shaped. A short allow-list of well-known configuration
// names wins over the substring rule.
func CredentialName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if credentialExact[upper] {
		return true
	}
	if nonCredentialExact[upper] {
		return false
	}
	for _, frag := range credentialSubstrings {
		if strings.Contains(upper, frag) {
			return true
		}
	}
	return false
}

// CredentialValue reports whether a value carries a credential that
// cannot be rewritten in place: a bearer header, a PEM block, or a JSON
// document with a token or password field. Such values must be dropped.
func CredentialValue(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	if strings.Contains(v, "-----BEGIN ") {
		return true
	}
	if len(v) > len("bearer ") && strings.EqualFold(v[:len("bearer ")], "bearer ") {
		return true
	}
	if strings.HasPrefix(v, "{") {
		var doc map[string]any
		if json.Unmarshal([]byte(v), &doc) != nil {
			return false
		}
		return credentialFieldIn(doc, jsonScanDepth)
	}
	return false
}

// RedactValue replaces the userinfo of a URL or DSN value with
// "redacted", keeping the scheme, host and path. Values that are not
// URL-shaped come back unchanged.
func RedactValue(value string) string {
	if !strings.Contains(value, "://") || !strings.Contains(value, "@") {
		return value
	}
	if redacted := sourceurl.Redact(value); redacted != "" {
		return redacted
	}
	return value
}

func credentialFieldIn(doc map[string]any, depth int) bool {
	if depth <= 0 {
		return false
	}
	for k, v := range doc {
		upper := strings.ToUpper(k)
		for _, frag := range jsonCredentialFields {
			if strings.Contains(upper, frag) {
				return true
			}
		}
		if nested, ok := v.(map[string]any); ok && credentialFieldIn(nested, depth-1) {
			return true
		}
	}
	return false
}
