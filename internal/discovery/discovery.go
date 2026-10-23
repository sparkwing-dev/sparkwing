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

type services struct {
	Dashboard string `json:"dashboard,omitempty"`

	MultiTeam bool `json:"multi_team,omitempty"`
}

var errNoController = errors.New("discovery: no controller URL configured")

type cacheKey struct {
	URL   string
	Token string
}

type cacheEntry struct {
	services services
	err      error
	at       time.Time
}

var (
	cacheMu sync.Mutex

	servicesCache = map[cacheKey]cacheEntry{}
)

const successTTL = 10 * time.Minute

const failureTTL = 30 * time.Second

func servicesFor(ctx context.Context, controllerURL, token string) (services, error) {
	if controllerURL == "" {
		return services{}, errNoController
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

func resetCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	servicesCache = map[cacheKey]cacheEntry{}
}

func fetchServices(ctx context.Context, controllerURL, token string) (services, error) {
	url := controllerURL + "/api/v1/services"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return services{}, fmt.Errorf("build services request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return services{}, fmt.Errorf("controller services: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var out services
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return services{}, fmt.Errorf("decode services response: %w", err)
		}
		return out, nil
	case http.StatusNotFound:
		return services{}, nil
	default:
		return services{}, fmt.Errorf("controller services: HTTP %d", resp.StatusCode)
	}
}
