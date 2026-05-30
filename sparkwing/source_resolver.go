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
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

// NewSecretResolverFromSpec builds a SecretResolver for the secrets
// surface from a backends.Spec. The returned resolver is uncached and
// unmasked; callers wrap with secrets.NewCached + masker (the
// orchestrator does this at the existing WithSecretResolver install
// site) to integrate with the run's log redaction.
//
// Supported types on the secrets surface:
//
//   - controller: HTTPS GET against spec.URL + /api/v1/secrets/<name>
//     using spec.ResolvedToken() in the Authorization header.
//   - filesystem: reads a dotenv file at spec.Path. Keys without
//     values resolve to ErrSecretMissing rather than the empty string.
//   - env: looks up os.Getenv(spec.Prefix + name). An unset env var
//     resolves to ErrSecretMissing.
//
// ctx is currently retained only for parity with future backends
// that may need it during construction.
func NewSecretResolverFromSpec(_ context.Context, spec backends.Spec) (SecretResolver, error) {
	switch spec.Type {
	case backends.TypeController:
		if spec.URL == "" {
			return nil, fmt.Errorf("secrets backend type=controller: url is empty")
		}
		return newRemoteControllerResolver(spec), nil
	case backends.TypeFilesystem:
		return newFileResolver(spec)
	case backends.TypeEnv:
		return newEnvResolver(spec), nil
	case "":
		return nil, fmt.Errorf("secrets backend: type is required")
	default:
		return nil, fmt.Errorf("secrets backend: unsupported type %q (controller | filesystem | env)", spec.Type)
	}
}

// remoteControllerResolver hits the controller's secrets endpoint.
// Values come back with the controller-declared masked flag so the
// Secret/Config strict-classification check at the SDK call site
// stays honest.
type remoteControllerResolver struct {
	spec   backends.Spec
	client *http.Client
}

func newRemoteControllerResolver(spec backends.Spec) *remoteControllerResolver {
	return &remoteControllerResolver{spec: spec, client: http.DefaultClient}
}

func (r *remoteControllerResolver) Resolve(ctx context.Context, name string) (string, bool, error) {
	base := strings.TrimRight(r.spec.URL, "/")
	if base == "" {
		return "", false, fmt.Errorf("secrets backend: url is empty")
	}
	endpoint := base + "/api/v1/secrets/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false, fmt.Errorf("secrets backend %s: build request: %w", base, err)
	}
	if tok := r.spec.ResolvedToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("secrets backend %s: %w", base, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return "", false, ErrSecretMissing
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", false, fmt.Errorf("secrets backend %s: %d %s", base, resp.StatusCode, http.StatusText(resp.StatusCode))
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", false, fmt.Errorf("secrets backend %s: %d %s: %s", base, resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(body)))
	}
	var body struct {
		Value  string `json:"value"`
		Masked bool   `json:"masked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, fmt.Errorf("secrets backend %s: decode response: %w", base, err)
	}
	return body.Value, body.Masked, nil
}

// fileResolver reads KEY=value pairs from a dotenv file. Values are
// reported as masked=true (the file is intended for secrets); use a
// separate env backend for non-secret config that should render
// unmasked.
type fileResolver struct {
	spec  backends.Spec
	mu    sync.Mutex
	once  sync.Once
	cache map[string]string
	err   error
}

func newFileResolver(spec backends.Spec) (*fileResolver, error) {
	if spec.Path == "" {
		return nil, fmt.Errorf("secrets backend type=filesystem: path is empty")
	}
	return &fileResolver{spec: spec}, nil
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
	raw, err := os.Open(f.spec.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			f.cache = map[string]string{}
			return
		}
		f.err = fmt.Errorf("secrets backend filesystem %s: %w", f.spec.Path, err)
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
		if n := len(val); n >= 2 {
			if (val[0] == '"' && val[n-1] == '"') || (val[0] == '\'' && val[n-1] == '\'') {
				val = val[1 : n-1]
			}
		}
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		f.err = fmt.Errorf("secrets backend filesystem %s: scan: %w", f.spec.Path, err)
		return
	}
	f.cache = out
}

// envResolver looks up os.Getenv(Prefix + name). Empty values resolve
// to ErrSecretMissing.
type envResolver struct {
	spec backends.Spec
}

func newEnvResolver(spec backends.Spec) *envResolver {
	return &envResolver{spec: spec}
}

func (e *envResolver) Resolve(_ context.Context, name string) (string, bool, error) {
	key := e.spec.Prefix + name
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return "", false, ErrSecretMissing
	}
	return v, true, nil
}
