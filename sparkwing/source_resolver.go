package sparkwing

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/sources"
)

// ProfileLookup fetches the controller URL and bearer token for a
// named profile (matching profiles.yaml). The SDK consumes it
// through a callback so the sparkwing package doesn't have to
// import the profile package directly.
//
// The lookup is called exactly once per NewSecretResolverFromSource
// for type=remote-controller sources; the returned resolver caches
// the result internally and reuses it for every Resolve call.
type ProfileLookup func(name string) (controller string, token string, err error)

// NewSecretResolverFromSource builds a SecretResolver tailored to
// the given source spec. The returned resolver is uncached and
// unmasked; callers wrap with secrets.NewCached + masker (the
// orchestrator does this at the existing WithSecretResolver install
// site) to integrate with the run's log redaction.
//
// Supported source types:
//
//   - remote-controller: HTTPS GET against the profile's
//     /api/v1/secrets/<name> endpoint with the profile's token in
//     the Authorization header. The profileLookup callback resolves
//     the named profile to (controller, token).
//   - macos-keychain: invokes /usr/bin/security find-generic-password
//     against the configured Service. Returns an actionable error on
//     non-darwin GOOS so misconfigured cluster runs fail loudly.
//   - file: reads a dotenv file at Path. Keys without values resolve
//     to ErrSecretMissing rather than the empty string.
//   - env: looks up os.Getenv(Prefix + name). An unset env var
//     resolves to ErrSecretMissing.
//
// ctx is currently retained only for parity with future backends
// that may need it during construction (HTTP transport tuning, OIDC
// token refresh). The current backends construct synchronously.
func NewSecretResolverFromSource(_ context.Context, src sources.Source, profileLookup ProfileLookup) (SecretResolver, error) {
	switch src.Type {
	case sources.TypeRemoteController:
		if profileLookup == nil {
			return nil, fmt.Errorf("source %q: type=%s requires a profile lookup", src.Name, src.Type)
		}
		controller, token, err := profileLookup(src.Controller)
		if err != nil {
			return nil, fmt.Errorf("source %q: profile lookup for %q: %w", src.Name, src.Controller, err)
		}
		return newRemoteControllerResolver(src, controller, token), nil
	case sources.TypeMacosKeychain:
		return newMacosKeychainResolver(src), nil
	case sources.TypeFile:
		return newFileResolver(src)
	case sources.TypeEnv:
		return newEnvResolver(src), nil
	case "":
		return nil, fmt.Errorf("source %q: type is required", src.Name)
	default:
		return nil, fmt.Errorf("source %q: unknown type %q", src.Name, src.Type)
	}
}

// remoteControllerResolver hits the controller's secrets endpoint.
// Values come back with the controller-declared masked flag so the
// Secret/Config strict-classification check at the SDK call site
// stays honest.
type remoteControllerResolver struct {
	src        sources.Source
	controller string
	token      string
	client     *http.Client
}

func newRemoteControllerResolver(src sources.Source, controller, token string) *remoteControllerResolver {
	return &remoteControllerResolver{
		src:        src,
		controller: strings.TrimRight(controller, "/"),
		token:      token,
		client:     http.DefaultClient,
	}
}

func (r *remoteControllerResolver) Resolve(ctx context.Context, name string) (string, bool, error) {
	if r.controller == "" {
		return "", false, fmt.Errorf("source %q: controller URL is empty", r.src.Name)
	}
	endpoint := r.controller + "/api/v1/secrets/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false, fmt.Errorf("source %q: build request: %w", r.src.Name, err)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("source %q: %w", r.src.Name, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusNotFound:
		return "", false, ErrSecretMissing
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", false, fmt.Errorf("source %q: %d %s", r.src.Name, resp.StatusCode, http.StatusText(resp.StatusCode))
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", false, fmt.Errorf("source %q: %d %s: %s", r.src.Name, resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(body)))
	}
	var body struct {
		Value  string `json:"value"`
		Masked bool   `json:"masked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, fmt.Errorf("source %q: decode response: %w", r.src.Name, err)
	}
	return body.Value, body.Masked, nil
}

// macosKeychainResolver shells out to /usr/bin/security on darwin.
// Reports a clear error on other GOOS so cluster runs that
// accidentally bind to a laptop-only source surface a useful message
// instead of hanging.
type macosKeychainResolver struct {
	src sources.Source
}

func newMacosKeychainResolver(src sources.Source) *macosKeychainResolver {
	return &macosKeychainResolver{src: src}
}

func (k *macosKeychainResolver) Resolve(_ context.Context, name string) (string, bool, error) {
	if goruntime.GOOS != "darwin" {
		return "", false, fmt.Errorf("source %q: macos-keychain is available only on darwin (current: %s)", k.src.Name, goruntime.GOOS)
	}
	cmd := exec.Command("/usr/bin/security", "find-generic-password",
		"-s", k.src.Service,
		"-a", name,
		"-w") // print only the password to stdout
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// security returns 44 when the item is missing.
			if exitErr.ExitCode() == 44 {
				return "", false, ErrSecretMissing
			}
			return "", false, fmt.Errorf("source %q: security exited %d: %s", k.src.Name, exitErr.ExitCode(), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", false, fmt.Errorf("source %q: %w", k.src.Name, err)
	}
	return strings.TrimRight(string(out), "\n"), true, nil
}

// fileResolver reads KEY=value pairs from a dotenv file. Values are
// reported as masked=true (the file is intended for secrets); use a
// separate env source for non-secret config that should render
// unmasked.
type fileResolver struct {
	src   sources.Source
	mu    sync.Mutex
	once  sync.Once
	cache map[string]string
	err   error
}

func newFileResolver(src sources.Source) (*fileResolver, error) {
	if src.Path == "" {
		return nil, fmt.Errorf("source %q: path is empty", src.Name)
	}
	return &fileResolver{src: src}, nil
}

func (f *fileResolver) Resolve(_ context.Context, name string) (string, bool, error) {
	f.once.Do(f.load)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", false, f.err
	}
	v, ok := f.cache[name]
	if !ok {
		return "", false, ErrSecretMissing
	}
	return v, true, nil
}

func (f *fileResolver) load() {
	raw, err := os.Open(f.src.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Treat as empty file -- resolutions hit ErrSecretMissing.
			f.cache = map[string]string{}
			return
		}
		f.err = fmt.Errorf("source %q: read %s: %w", f.src.Name, f.src.Path, err)
		return
	}
	defer raw.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(raw)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// Strip surrounding quotes if present.
		if n := len(val); n >= 2 {
			if (val[0] == '"' && val[n-1] == '"') || (val[0] == '\'' && val[n-1] == '\'') {
				val = val[1 : n-1]
			}
		}
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		f.err = fmt.Errorf("source %q: scan %s: %w", f.src.Name, f.src.Path, err)
		return
	}
	f.cache = out
}

// envResolver looks up os.Getenv(Prefix + name). Empty values resolve
// to ErrSecretMissing so callers can distinguish "explicitly set to
// empty string" (which the env layer can't represent anyway) from
// "not set." Values are masked=true; env-backed secrets are intended
// to carry the same redaction guarantees as the dotenv variant.
type envResolver struct {
	src sources.Source
}

func newEnvResolver(src sources.Source) *envResolver {
	return &envResolver{src: src}
}

func (e *envResolver) Resolve(_ context.Context, name string) (string, bool, error) {
	key := e.src.Prefix + name
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return "", false, ErrSecretMissing
	}
	return v, true, nil
}
