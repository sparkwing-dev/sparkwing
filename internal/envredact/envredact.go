// Package envredact classifies environment variables that must not be
// persisted verbatim. It matches on the name, on the value shape, and
// rewrites URL and DSN userinfo so a stored environment keeps the host
// without the password.
package envredact

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode"

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
	"JWT",
	"COOKIE",
	"SIGNATURE",
}

var credentialSegments = map[string]bool{
	"PAT": true,
	"SIG": true,
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
	"SSH_AUTH_SOCK":                         true,
	"DOCKER_TLS_CERTDIR":                    true,
	"DOCKER_CERT_PATH":                      true,
	"SPARKWING_REQUIRE_AUTH":                true,
	"SPARKWING_CACHE_ALLOW_UNAUTHENTICATED": true,
}

const jsonScanDepth = 8

const bearerScheme = "BEARER "

const redactedPlaceholder = "redacted"

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
	return credentialShaped(upper)
}

// CredentialValue reports whether a value carries a credential that
// cannot be rewritten in place: a bearer header, a PEM block, a JSON
// document with a credential-shaped field, or a URL whose query or path
// names one. Every line of a multi-line value is examined. Such values
// must be dropped.
func CredentialValue(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	if strings.Contains(v, "-----BEGIN ") {
		return true
	}
	if credentialJSON(v) {
		return true
	}
	for _, line := range strings.Split(v, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if bearerIn(line) || credentialJSON(line) || credentialURL(line) {
			return true
		}
	}
	return false
}

// RedactValue replaces the userinfo of a URL or DSN value with
// "redacted", and replaces any query parameter value or path segment the
// URL names like a credential. Values that are not URL-shaped come back
// unchanged.
func RedactValue(value string) string {
	out := value
	if strings.Contains(out, "://") && strings.Contains(out, "@") {
		if redacted := sourceurl.Redact(out); redacted != "" {
			out = redacted
		}
	}
	return redactURLCredentials(out)
}

func credentialShaped(name string) bool {
	upper := strings.ToUpper(name)
	for _, frag := range credentialSubstrings {
		if strings.Contains(upper, frag) {
			return true
		}
	}
	for _, seg := range strings.FieldsFunc(upper, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if credentialSegments[seg] {
			return true
		}
	}
	return false
}

func bearerIn(line string) bool {
	upper := strings.ToUpper(line)
	for i := 0; i+len(bearerScheme) <= len(upper); i++ {
		if upper[i:i+len(bearerScheme)] != bearerScheme {
			continue
		}
		if i > 0 && isNameByte(upper[i-1]) {
			continue
		}
		if strings.TrimSpace(line[i+len(bearerScheme):]) != "" {
			return true
		}
	}
	return false
}

func isNameByte(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func credentialJSON(value string) bool {
	if !strings.HasPrefix(value, "{") {
		return false
	}
	var doc map[string]any
	if json.Unmarshal([]byte(value), &doc) != nil {
		return false
	}
	return credentialFieldIn(doc, jsonScanDepth)
}

func credentialURL(value string) bool {
	u := parseCredentialURL(value)
	if u == nil {
		return false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg != "" && credentialShaped(seg) {
			return true
		}
	}
	for _, pair := range strings.Split(u.RawQuery, "&") {
		if name, _, ok := strings.Cut(pair, "="); ok && credentialShaped(name) {
			return true
		}
	}
	return false
}

func redactURLCredentials(value string) string {
	u := parseCredentialURL(value)
	if u == nil {
		return value
	}
	changed := false
	if u.RawQuery != "" {
		pairs := strings.Split(u.RawQuery, "&")
		for i, pair := range pairs {
			name, _, ok := strings.Cut(pair, "=")
			if ok && credentialShaped(name) {
				pairs[i] = name + "=" + redactedPlaceholder
				changed = true
			}
		}
		if changed {
			u.RawQuery = strings.Join(pairs, "&")
		}
	}
	segments := strings.Split(u.Path, "/")
	for i, seg := range segments {
		if seg != "" && credentialShaped(seg) {
			segments[i] = redactedPlaceholder
			changed = true
		}
	}
	if !changed {
		return value
	}
	u.Path = strings.Join(segments, "/")
	u.RawPath = ""
	return u.String()
}

func parseCredentialURL(value string) *url.URL {
	if !strings.Contains(value, "://") {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return nil
	}
	return u
}

func credentialFieldIn(doc map[string]any, depth int) bool {
	if depth <= 0 {
		return false
	}
	for k, v := range doc {
		if credentialShaped(k) {
			return true
		}
		if nested, ok := v.(map[string]any); ok && credentialFieldIn(nested, depth-1) {
			return true
		}
	}
	return false
}
