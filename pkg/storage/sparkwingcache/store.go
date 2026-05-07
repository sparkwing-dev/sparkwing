// Package sparkwingcache adapts the sparkwing-cache HTTP /bin/<key>
// endpoints to storage.ArtifactStore. Keys are treated as opaque.
package sparkwingcache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/v2/pkg/storage"
)

// Store implements storage.ArtifactStore over the sparkwing-cache
// /bin/<key> HTTP endpoints.
type Store struct {
	baseURL string
	token   string
	http    *http.Client
}

// New constructs a Store. token is sent as a Bearer header on writes
// (cache enforces auth on PUT; GET is unauthenticated). nil
// httpClient uses a default with a 60s timeout.
func New(baseURL, token string, httpClient *http.Client) *Store {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Store{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpClient,
	}
}

var _ storage.ArtifactStore = (*Store)(nil)

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.binURL(key), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, storage.ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("sparkwingcache GET %s: %s: %s",
			key, resp.Status, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}

func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	// Buffer: cache server requires Content-Length (no chunked PUTs).
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.binURL(key), bytes.NewReader(data))
	if err != nil {
		return err
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("sparkwingcache PUT %s: %s: %s",
			key, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *Store) Has(ctx context.Context, key string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.binURL(key), nil)
	if err != nil {
		return false, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		// Fall back to GET on servers that don't support HEAD.
		if errors.Is(err, http.ErrNotSupported) {
			rc, gerr := s.Get(ctx, key)
			if errors.Is(gerr, storage.ErrNotFound) {
				return false, nil
			}
			if gerr != nil {
				return false, gerr
			}
			rc.Close()
			return true, nil
		}
		return false, err
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("sparkwingcache HEAD %s: %s", key, resp.Status)
	}
}

func (s *Store) Delete(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.binURL(key), nil)
	if err != nil {
		return err
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("sparkwingcache DELETE %s: %s: %s",
			key, resp.Status, strings.TrimSpace(string(body)))
	}
}

func (s *Store) binURL(key string) string {
	return s.baseURL + "/bin/" + key
}

// List is unsupported; the cache server has no enumeration endpoint.
// Callers that need listing use fs / s3 backends.
func (s *Store) List(context.Context, string) ([]string, error) {
	return nil, storage.ErrListNotSupported
}
