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

	"github.com/sparkwing-dev/sparkwing/pkg/sources"
)

// NewSecretResolverFromSource builds a SecretResolver tailored to
// the given source spec. The returned resolver is uncached and
// unmasked; callers wrap with secrets.NewCached + masker (the
// orchestrator does this at the existing WithSecretResolver install
// site) to integrate with the run's log redaction.
//
// Supported source types:
//
//   - controller: HTTPS GET against src.URL + /api/v1/secrets/<name>
//     with controllerToken in the Authorization header. The caller is
//     responsible for any URL-match policy against the active profile
//     before passing the token through.
//   - file: reads a dotenv file at Path. Keys without values resolve
//     to ErrSecretMissing rather than the empty string.
//   - env: looks up os.Getenv(Prefix + name). An unset env var
//     resolves to ErrSecretMissing.
//
// controllerToken is consumed only for type=controller; file and env
// ignore it.
//
// ctx is currently retained only for parity with future backends
// that may need it during construction.
func NewSecretResolverFromSource(_ context.Context, src sources.Source, controllerToken string) (SecretResolver, error) {
	switch src.Type {
	case sources.TypeController:
		if src.URL == "" {
			return nil, fmt.Errorf("source %s: url is empty", src.Describe())
		}
		return newRemoteControllerResolver(src, controllerToken), nil
	case sources.TypeFile:
		return newFileResolver(src)
	case sources.TypeEnv:
		return newEnvResolver(src), nil
	case "":
		return nil, fmt.Errorf("source: type is required")
	default:
		return nil, fmt.Errorf("source %s: unknown type %q", src.Describe(), src.Type)
	}
}

// remoteControllerResolver hits the controller's secrets endpoint.
// Values come back with the controller-declared masked flag so the
// Secret/Config strict-classification check at the SDK call site
// stays honest.
type remoteControllerResolver struct {
	src    sources.Source
	token  string
	client *http.Client
}

func newRemoteControllerResolver(src sources.Source, token string) *remoteControllerResolver {
	return &remoteControllerResolver{
		src:    src,
		token:  token,
		client: http.DefaultClient,
	}
}

func (r *remoteControllerResolver) Resolve(ctx context.Context, name string) (string, bool, error) {
	base := strings.TrimRight(r.src.URL, "/")
	if base == "" {
		return "", false, fmt.Errorf("source %s: url is empty", r.src.Describe())
	}
	endpoint := base + "/api/v1/secrets/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", false, fmt.Errorf("source %s: build request: %w", r.src.Describe(), err)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("source %s: %w", r.src.Describe(), err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusNotFound:
		return "", false, ErrSecretMissing
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", false, fmt.Errorf("source %s: %d %s", r.src.Describe(), resp.StatusCode, http.StatusText(resp.StatusCode))
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", false, fmt.Errorf("source %s: %d %s: %s", r.src.Describe(), resp.StatusCode, http.StatusText(resp.StatusCode), strings.TrimSpace(string(body)))
	}
	var body struct {
		Value  string `json:"value"`
		Masked bool   `json:"masked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, fmt.Errorf("source %s: decode response: %w", r.src.Describe(), err)
	}
	return body.Value, body.Masked, nil
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
		return nil, fmt.Errorf("source %s: path is empty", src.Describe())
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
		f.err = fmt.Errorf("source %s: read %s: %w", f.src.Describe(), f.src.Path, err)
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
		f.err = fmt.Errorf("source %s: scan %s: %w", f.src.Describe(), f.src.Path, err)
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
