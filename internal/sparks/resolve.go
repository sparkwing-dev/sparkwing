package sparks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

const DefaultGoProxy = "https://proxy.golang.org"

var proxyClient = &http.Client{Timeout: 30 * time.Second}

type Resolver struct {
	Proxies []string

	Private []string

	HTTPClient *http.Client

	GoBin string
}

func Resolve(ctx context.Context, m *Manifest) (map[string]string, error) {
	r := NewResolverFromEnv()
	return r.Resolve(ctx, m)
}

func NewResolverFromEnv() *Resolver {
	var proxies []string
	for _, p := range strings.Split(goEnvValue("GOPROXY"), ",") {
		p = strings.TrimSpace(p)
		if p == "" || p == "direct" || p == "off" {
			continue
		}
		proxies = append(proxies, strings.TrimRight(p, "/"))
	}
	if len(proxies) == 0 {
		proxies = []string{DefaultGoProxy}
	}
	var private []string
	for _, p := range strings.Split(goEnvValue("GOPRIVATE"), ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			private = append(private, p)
		}
	}
	return &Resolver{Proxies: proxies, Private: private}
}

func goEnvValue(name string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	cmd := exec.Command("go", "env", name)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (r *Resolver) Resolve(ctx context.Context, m *Manifest) (map[string]string, error) {
	if m == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(m.Libraries))
	for _, lib := range m.Libraries {
		ver, err := r.resolveOne(ctx, lib)
		if err != nil {
			return nil, fmt.Errorf("sparks: resolve %s@%s: %w", lib.Source, lib.Version, err)
		}
		out[lib.Source] = ver
	}
	return out, nil
}

func (r *Resolver) resolveOne(ctx context.Context, lib Library) (string, error) {
	// safety: the source reaches `go list` as a module argument, so it must be a module path.
	if err := module.CheckPath(lib.Source); err != nil {
		return "", fmt.Errorf("invalid source %q: %w", lib.Source, err)
	}
	if semver.IsValid(lib.Version) && !isRangePrefix(lib.Version) {
		return lib.Version, nil
	}
	if r.isPrivate(lib.Source) {
		return r.resolveViaGoList(ctx, lib)
	}
	if strings.EqualFold(strings.TrimSpace(lib.Version), "latest") {
		return r.resolveLatest(ctx, lib.Source)
	}
	return r.resolveRange(ctx, lib.Source, lib.Version)
}

func isRangePrefix(v string) bool {
	if v == "" {
		return false
	}
	switch v[0] {
	case '^', '~':
		return true
	}
	return strings.HasPrefix(v, ">=") || strings.HasPrefix(v, ">") ||
		strings.HasPrefix(v, "<=") || strings.HasPrefix(v, "<") ||
		strings.EqualFold(v, "latest")
}

func (r *Resolver) isPrivate(modPath string) bool {
	if len(r.Private) == 0 {
		return false
	}
	return module.MatchPrefixPatterns(strings.Join(r.Private, ","), modPath)
}

func (r *Resolver) httpc() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return proxyClient
}

func (r *Resolver) resolveLatest(ctx context.Context, modPath string) (string, error) {
	escaped, err := module.EscapePath(modPath)
	if err != nil {
		return "", fmt.Errorf("escape module path: %w", err)
	}
	var lastErr error
	for _, proxy := range r.activeProxies() {
		u := proxy + "/" + path.Join(escaped, "@latest")
		body, err := r.proxyGET(ctx, u)
		if err != nil {
			lastErr = err
			continue
		}
		var info struct {
			Version string `json:"Version"`
		}
		if err := json.Unmarshal(body, &info); err != nil {
			lastErr = fmt.Errorf("decode @latest for %s: %w", modPath, err)
			continue
		}
		if info.Version == "" {
			lastErr = fmt.Errorf("@latest for %s returned empty Version", modPath)
			continue
		}
		return info.Version, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no proxy configured")
	}
	return "", lastErr
}

func (r *Resolver) resolveRange(ctx context.Context, modPath, constraint string) (string, error) {
	escaped, err := module.EscapePath(modPath)
	if err != nil {
		return "", fmt.Errorf("escape module path: %w", err)
	}
	var lastErr error
	for _, proxy := range r.activeProxies() {
		u := proxy + "/" + path.Join(escaped, "@v", "list")
		body, err := r.proxyGET(ctx, u)
		if err != nil {
			lastErr = err
			continue
		}
		versions := parseVersionList(string(body))
		if len(versions) == 0 {
			lastErr = fmt.Errorf("no versions listed for %s", modPath)
			continue
		}
		best, err := pickBest(versions, constraint)
		if err != nil {
			return "", err
		}
		return best, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no proxy configured")
	}
	return "", lastErr
}

func (r *Resolver) resolveViaGoList(ctx context.Context, lib Library) (string, error) {
	bin := r.GoBin
	if bin == "" {
		bin = "go"
	}
	query := lib.Version
	if isRangePrefix(query) && !strings.EqualFold(query, "latest") {
		query = "latest"
	}
	cmd := exec.CommandContext(ctx, bin, "list", "-m", "-json", lib.Source+"@"+query)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list -m %s@%s: %w", lib.Source, query, err)
	}
	var info struct {
		Version string `json:"Version"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", fmt.Errorf("decode go list output: %w", err)
	}
	if info.Version == "" {
		return "", fmt.Errorf("go list returned empty version for %s", lib.Source)
	}
	if isRangePrefix(lib.Version) && !strings.EqualFold(lib.Version, "latest") {
		if !constraintAllows(lib.Version, info.Version) {
			return "", fmt.Errorf("latest %s of %s does not satisfy %s",
				info.Version, lib.Source, lib.Version)
		}
	}
	return info.Version, nil
}

func (r *Resolver) activeProxies() []string {
	if len(r.Proxies) > 0 {
		return r.Proxies
	}
	return []string{DefaultGoProxy}
}

func (r *Resolver) proxyGET(ctx context.Context, rawURL string) ([]byte, error) {
	if _, err := url.Parse(rawURL); err != nil {
		return nil, fmt.Errorf("bad url %q: %w", rawURL, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.httpc().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return nil, fmt.Errorf("proxy %s -> %s", rawURL, resp.Status)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("proxy %s -> %s", rawURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return body, nil
}

func parseVersionList(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !semver.IsValid(line) {
			continue
		}
		out = append(out, line)
	}
	sort.Slice(out, func(i, j int) bool {
		return semver.Compare(out[i], out[j]) > 0
	})
	return out
}

func pickBest(versions []string, constraint string) (string, error) {
	for _, v := range versions {
		if semver.Prerelease(v) != "" {
			continue
		}
		if constraintAllows(constraint, v) {
			return v, nil
		}
	}
	return "", fmt.Errorf("no version satisfies %s", constraint)
}

func constraintAllows(constraint, v string) bool {
	c := strings.TrimSpace(constraint)
	if c == "" || strings.EqualFold(c, "latest") {
		return true
	}
	if !semver.IsValid(v) {
		return false
	}
	switch {
	case strings.HasPrefix(c, "^"):
		base := c[1:]
		return semver.IsValid(base) &&
			semver.Major(v) == semver.Major(base) &&
			semver.Compare(v, base) >= 0
	case strings.HasPrefix(c, "~"):
		base := c[1:]
		return semver.IsValid(base) &&
			semver.MajorMinor(v) == semver.MajorMinor(base) &&
			semver.Compare(v, base) >= 0
	case strings.HasPrefix(c, ">="):
		base := strings.TrimSpace(c[2:])
		return semver.IsValid(base) && semver.Compare(v, base) >= 0
	case strings.HasPrefix(c, "<="):
		base := strings.TrimSpace(c[2:])
		return semver.IsValid(base) && semver.Compare(v, base) <= 0
	case strings.HasPrefix(c, ">"):
		base := strings.TrimSpace(c[1:])
		return semver.IsValid(base) && semver.Compare(v, base) > 0
	case strings.HasPrefix(c, "<"):
		base := strings.TrimSpace(c[1:])
		return semver.IsValid(base) && semver.Compare(v, base) < 0
	}
	if semver.IsValid(c) {
		return semver.Compare(v, c) == 0
	}
	return false
}
