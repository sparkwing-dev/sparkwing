// Package directdata handles the short control requests and the separate S3
// transfer used by direct cache uploads and downloads.
package directdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrUnavailable = errors.New("direct data routes unavailable")
	ErrNotFound    = errors.New("direct object not found")
	ErrExists      = errors.New("direct object already committed")
)

type Client struct {
	base   string
	bearer string
	runID  string
	http   *http.Client
}

func New(baseURL, bearer, runID string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), bearer: bearer, runID: runID, http: client}
}

func (c *Client) Available(ctx context.Context) (bool, error) {
	if c.base == "" || c.bearer == "" || c.runID == "" {
		return false, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/data/capabilities", nil)
	if err != nil {
		return false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("data capabilities: %s", resp.Status)
	}
	var caps struct {
		Upload   bool `json:"upload"`
		Download bool `json:"download"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&caps); err != nil {
		return false, err
	}
	if !caps.Upload || !caps.Download {
		return false, ErrUnavailable
	}
	return true, nil
}

type uploadAnswer struct {
	UploadID string            `json:"upload_id"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
}

func (c *Client) control(ctx context.Context, path string, body, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.bearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, ErrNotFound
	}
	if path == "/api/v1/data/upload" && resp.StatusCode == http.StatusConflict {
		return resp.StatusCode, ErrExists
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return resp.StatusCode, fmt.Errorf("direct data %s: %s: %s", path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// Upload asks for a pending URL, sends exactly size bytes with its signed
// headers, and commits the object before returning.
func (c *Client) Upload(ctx context.Context, kind, key string, body io.ReadSeeker, size int64, sha string) error {
	var answer uploadAnswer
	_, err := c.control(ctx, "/api/v1/data/upload", map[string]any{
		"kind": kind, "key": key, "size": size, "sha256": sha, "run_id": c.runID,
	}, &answer)
	if err != nil {
		return err
	}
	if answer.UploadID == "" || answer.URL == "" {
		return errors.New("direct upload answer is incomplete")
	}
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, answer.URL, body)
	if err != nil {
		return err
	}
	put.ContentLength = size
	for k, v := range answer.Headers {
		put.Header.Set(k, v)
	}
	resp, err := c.http.Do(put)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("direct data PUT: %s", resp.Status)
	}
	_, err = c.control(ctx, "/api/v1/data/commit", map[string]string{
		"upload_id": answer.UploadID, "run_id": c.runID,
	}, nil)
	return err
}

type DownloadAnswer struct {
	URL       string    `json:"url"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Download signs one committed object and opens its byte stream. The caller
// checks SHA256 while consuming it.
func (c *Client) Download(ctx context.Context, kind, key string) (io.ReadCloser, DownloadAnswer, error) {
	var answer DownloadAnswer
	_, err := c.control(ctx, "/api/v1/data/download", map[string]string{
		"kind": kind, "key": key,
	}, &answer)
	if err != nil {
		return nil, DownloadAnswer{}, err
	}
	parsed, err := url.Parse(answer.URL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || answer.SHA256 == "" {
		return nil, DownloadAnswer{}, errors.New("direct download answer is incomplete")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, answer.URL, nil)
	if err != nil {
		return nil, DownloadAnswer{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, DownloadAnswer{}, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, DownloadAnswer{}, fmt.Errorf("direct data GET: %s", resp.Status)
	}
	return resp.Body, answer, nil
}
