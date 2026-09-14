// Package envredact classifies environment variables that must not be
// persisted verbatim. It matches on the name, on the value shape, and
// rewrites URL and DSN userinfo so a stored environment keeps the host
// without the password. The same vocabulary classifies file names and
// file bytes, so a caller that ships a working tree elsewhere refuses
// the same credentials this package refuses to persist.
package envredact

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

var credentialWords = map[string]bool{
	"TOKEN":         true,
	"SECRET":        true,
	"PASSWORD":      true,
	"PASS":          true,
	"KEY":           true,
	"CREDENTIAL":    true,
	"DSN":           true,
	"AUTH":          true,
	"PRIVATE":       true,
	"PEM":           true,
	"CERT":          true,
	"JWT":           true,
	"COOKIE":        true,
	"SIGNATURE":     true,
	"PAT":           true,
	"SIG":           true,
	"PASSWD":        true,
	"PWD":           true,
	"AUTHORIZATION": true,
	"BEARER":        true,
	"PASSPHRASE":    true,
}

var credentialPrefixes = []string{
	"PASSWORD",
	"PASSWD",
	"SECRET",
	"TOKEN",
	"CERT",
	"PEM",
	"AUTH",
}

var credentialSuffixes = []string{
	"KEY",
	"SECRET",
	"TOKEN",
	"PASSWORD",
	"PASSWD",
	"PWD",
}

var credentialInfixes = []string{
	"PASSPHRASE",
}

var nonCredentialSegments = map[string]bool{
	"MONKEY":  true,
	"DONKEY":  true,
	"TURKEY":  true,
	"HOCKEY":  true,
	"JOCKEY":  true,
	"WHISKEY": true,
	"MICKEY":  true,
	"LACKEY":  true,
	"HOTKEY":  true,
	"OLDPWD":  true,
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
	"TOKENIZERS_PARALLELISM":                true,
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
	"PWD":                                   true,
	"OLDPWD":                                true,
}

var secretFileExtensions = map[string]bool{
	"pem":      true,
	"key":      true,
	"p12":      true,
	"pfx":      true,
	"pkcs12":   true,
	"jks":      true,
	"keystore": true,
	"ppk":      true,
	"kdbx":     true,
}

var secretFileNames = map[string]bool{
	"id_rsa":     true,
	"id_dsa":     true,
	"id_ecdsa":   true,
	"id_ed25519": true,
	".netrc":     true,
	"_netrc":     true,
	".pgpass":    true,
	".htpasswd":  true,
}

var credentialDataExtensions = map[string]bool{
	"":           true,
	"json":       true,
	"yaml":       true,
	"yml":        true,
	"txt":        true,
	"ini":        true,
	"conf":       true,
	"cfg":        true,
	"toml":       true,
	"properties": true,
	"secret":     true,
}

var nonSecretFileStems = map[string]bool{
	"authors":      true,
	"contributors": true,
	"maintainers":  true,
	"authorship":   true,
}

var contentScanExtensions = map[string]bool{
	"":           true,
	"env":        true,
	"txt":        true,
	"ini":        true,
	"conf":       true,
	"cfg":        true,
	"properties": true,
	"secret":     true,
}

const maxCredentialFileBytes = 64 << 10

const jsonScanDepth = 8

const bearerScheme = "BEARER "

const redactedPlaceholder = "redacted"

// CredentialName reports whether an environment variable name is
// credential-shaped: a name segment that is a credential word, that
// begins or ends with one such as CERTFILE or APIKEY, or that carries
// PASSPHRASE anywhere. A short allow-list of well-known configuration
// names wins over that rule.
func CredentialName(name string) bool {
	trimmed := strings.TrimSpace(name)
	upper := strings.ToUpper(trimmed)
	if credentialExact[upper] {
		return true
	}
	if nonCredentialExact[upper] {
		return false
	}
	return credentialShaped(trimmed)
}

// CredentialValue reports whether a value carries a credential that
// cannot be rewritten in place: a bearer header, a PEM block, a JSON
// document or array with a credential-shaped field, a KEY=VALUE
// assignment named like one, or a URL whose query or path names one.
// Every line of a multi-line value is examined. Such values must be
// dropped.
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
		if bearerIn(line) || credentialJSON(line) || credentialURL(line) || credentialAssignmentIn(line) {
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
	for _, seg := range nameSegments(name) {
		if credentialSegment(seg) {
			return true
		}
		if strings.HasSuffix(seg, "S") && credentialSegment(seg[:len(seg)-1]) {
			return true
		}
	}
	return false
}

func credentialSegment(seg string) bool {
	if nonCredentialSegments[seg] {
		return false
	}
	if credentialWords[seg] {
		return true
	}
	for _, prefix := range credentialPrefixes {
		if len(seg) > len(prefix) && strings.HasPrefix(seg, prefix) {
			return true
		}
	}
	for _, suffix := range credentialSuffixes {
		if len(seg) > len(suffix) && strings.HasSuffix(seg, suffix) {
			return true
		}
	}
	for _, infix := range credentialInfixes {
		if strings.Contains(seg, infix) {
			return true
		}
	}
	return false
}

func nameSegments(name string) []string {
	var out []string
	for _, run := range strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		out = appendCaseSegments(out, run)
	}
	return out
}

func appendCaseSegments(dst []string, run string) []string {
	r := []rune(run)
	start := 0
	for i := 1; i < len(r); i++ {
		if !unicode.IsUpper(r[i]) {
			continue
		}
		lowerRunStart := !unicode.IsUpper(r[i-1])
		acronymEnd := unicode.IsUpper(r[i-1]) && i+1 < len(r) && unicode.IsLower(r[i+1])
		if !lowerRunStart && !acronymEnd {
			continue
		}
		dst = append(dst, strings.ToUpper(string(r[start:i])))
		start = i
	}
	return append(dst, strings.ToUpper(string(r[start:])))
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
	if !strings.HasPrefix(value, "{") && !strings.HasPrefix(value, "[") {
		return false
	}
	var doc any
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

func credentialFieldIn(doc any, depth int) bool {
	if depth <= 0 {
		return false
	}
	switch node := doc.(type) {
	case map[string]any:
		for k, v := range node {
			if credentialShaped(k) || credentialFieldIn(v, depth-1) {
				return true
			}
		}
	case []any:
		for _, v := range node {
			if credentialFieldIn(v, depth-1) {
				return true
			}
		}
	}
	return false
}

func credentialAssignmentIn(line string) bool {
	for _, field := range strings.Fields(line) {
		name, value, ok := strings.Cut(field, "=")
		if !ok || value == "" || !envNameShaped(name) {
			continue
		}
		if CredentialName(name) {
			return true
		}
	}
	return false
}

func envNameShaped(name string) bool {
	if len(name) < 2 {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch {
		case b == '_':
		case b >= 'A' && b <= 'Z':
		case b >= '0' && b <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// CredentialFileName reports whether a file name is secret-shaped: a
// dotenv file, a key or keystore extension, a well-known private key
// name, or a configuration or data file whose stem is a
// credential-shaped name. A source file is never secret-shaped by its
// name, because a name such as auth.go describes code rather than
// carrying a secret.
func CredentialFileName(path string) bool {
	name, extension := fileNameAndExtension(path)
	if name == "" {
		return false
	}
	lower := strings.ToLower(name)
	if secretFileNames[lower] || dotenvName(lower) {
		return true
	}
	if secretFileExtensions[extension] {
		return true
	}
	if !credentialDataExtensions[extension] {
		return false
	}
	stem := name
	if extension != "" {
		stem = name[:len(name)-len(extension)-1]
	}
	if nonSecretFileStems[strings.ToLower(stem)] {
		return false
	}
	return CredentialName(stem)
}

// CredentialFileScannable reports whether a file of this name and size
// is worth reading for credential content. Only a small configuration
// or data file is: a compiled artifact or an archive has no line
// structure the value patterns can judge, and a large file is not the
// shape a leaked secret takes.
func CredentialFileScannable(path string, size int64) bool {
	return size > 0 && size <= maxCredentialFileBytes && contentScannedExtension(path)
}

// CredentialFileContent reports whether the bytes of a scannable file
// carry a credential. Binary content is never judged, because the value
// patterns read lines.
func CredentialFileContent(path string, content []byte) bool {
	if !CredentialFileScannable(path, int64(len(content))) {
		return false
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return false
	}
	return CredentialValue(string(content))
}

func contentScannedExtension(path string) bool {
	name, extension := fileNameAndExtension(path)
	if dotenvName(strings.ToLower(name)) {
		return true
	}
	return contentScanExtensions[extension]
}

func fileNameAndExtension(path string) (string, string) {
	name := path
	if cut := strings.LastIndexAny(name, "/\\"); cut >= 0 {
		name = name[cut+1:]
	}
	lower := strings.ToLower(name)
	extension := lower[strings.LastIndexByte(lower, '.')+1:]
	if extension == lower {
		extension = ""
	}
	return name, extension
}

func dotenvName(lower string) bool {
	return lower == ".env" || strings.HasPrefix(lower, ".env.") || strings.HasSuffix(lower, ".env")
}
