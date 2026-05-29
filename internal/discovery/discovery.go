// Package discovery queries the controller's GET /api/v1/services
// endpoint to discover auxiliary-service URLs (sparkwing-cache pod
// etc.) without requiring the operator to configure them per-profile.
//
// Results are cached per-process keyed by controller URL + token so a
// single CLI invocation that calls discovery from multiple paths
// (e.g. `sparkwing push` followed by an eager-refresh on dispatch)
// only pays one HTTP roundtrip.
//
// Callers that have no controller (no controller URL set on the
// active profile) skip discovery entirely -- the relevant operations
// (`sparkwing push`, eager-refresh) fail loudly with a clear message
// pointing the operator at adding a controller binding.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Services mirrors the controller's pkg/controller.ServicesResponse
// (re-declared here to avoid a cycle: cmd/sparkwing imports both
// internal/profile and internal/discovery; pkg/controller is the
// server side).
type Services struct {
	CachePod string `json:"cache_pod,omitempty"`
}

// ErrNoController is returned when ServicesFor is called with an
// empty controllerURL. Callers handle this with a clear message
// ("this command needs a controller-bound profile") rather than
// hiding the failure.
var ErrNoController = errors.New("discovery: no controller URL configured")

type cacheKey struct {
	URL   string
	Token string
}

type cacheEntry struct {
	services Services
	err      error
	at       time.Time
}

var (
	cacheMu sync.Mutex
	// servicesCache holds at most one entry per (URL, Token) pair for
	// the lifetime of the process. Discovery failures cache too (with
	// a shorter TTL) so a transient outage doesn't hammer the
	// controller from a chatty CLI session.
	servicesCache = map[cacheKey]cacheEntry{}
)

// successTTL is how long a successful discovery result stays cached
// in-process. Set generously: cache pod URL changes are infrastructure
// events, not per-run events.
const successTTL = 10 * time.Minute

// failureTTL is how long a discovery failure stays cached so the CLI
// doesn't retry on every operation in a session when the controller
// is down or doesn't implement /services yet.
const failureTTL = 30 * time.Second

// ServicesFor returns the controller's announced services for the
// given controller URL + token. Returns ErrNoController when
// controllerURL is empty; returns the cached result (success or
// failure) when the per-process cache holds a fresh entry.
//
// Network errors are returned wrapped; an HTTP 404 from /api/v1/services
// is reported as a zero-value Services + nil error so callers can
// branch on "no cache pod announced" without an error path.
func ServicesFor(ctx context.Context, controllerURL, token string) (Services, error) {
	if controllerURL == "" {
		return Services{}, ErrNoController
	}
	key := cacheKey{URL: controllerURL, Token: token}

	cacheMu.Lock()
	if entry, ok := servicesCache[key]; ok {
		ttl := successTTL
		if entry.err != nil {
			ttl = failureTTL
		}
		if time.Since(entry.at) < ttl {
			cacheMu.Unlock()
			return entry.services, entry.err
		}
	}
	cacheMu.Unlock()

	svc, err := fetchServices(ctx, controllerURL, token)

	cacheMu.Lock()
	servicesCache[key] = cacheEntry{services: svc, err: err, at: time.Now()}
	cacheMu.Unlock()
	return svc, err
}

// ResetCache clears the per-process services cache. Tests use this
// between cases that point at different fake controllers.
func ResetCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	servicesCache = map[cacheKey]cacheEntry{}
}

func fetchServices(ctx context.Context, controllerURL, token string) (Services, error) {
	url := controllerURL + "/api/v1/services"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Services{}, fmt.Errorf("build services request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Services{}, fmt.Errorf("controller services: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var out Services
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return Services{}, fmt.Errorf("decode services response: %w", err)
		}
		return out, nil
	case http.StatusNotFound:
		// Either the endpoint isn't registered (older controller)
		// or cache_pod_url wasn't set. Both mean "no auxiliary
		// services" -- the right thing for the caller to do is
		// fall through with whatever explicit config it has.
		return Services{}, nil
	default:
		return Services{}, fmt.Errorf("controller services: HTTP %d", resp.StatusCode)
	}
}
