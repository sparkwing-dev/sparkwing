package releaseasset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// HTTPStatusError reports a failed release request.
type HTTPStatusError struct{ Code int }

func (e HTTPStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Code) }

// Download fetches release bytes, using GitHub's asset API after a web-route
// server failure or timeout. Callers must authenticate the returned bytes.
// A zero maxBytes leaves the body size unrestricted.
func Download(ctx context.Context, client *http.Client, address string, maxBytes int64) ([]byte, error) {
	fetch := func(address, accept string, limit int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, HTTPStatusError{Code: resp.StatusCode}
		}
		var reader io.Reader = resp.Body
		if limit > 0 {
			reader = io.LimitReader(reader, limit+1)
		}
		body, err := io.ReadAll(reader)
		if err == nil && limit > 0 && int64(len(body)) > limit {
			err = fmt.Errorf("download exceeds %d-byte limit", limit)
		}
		return body, err
	}
	body, err := fetch(address, "application/octet-stream", maxBytes)
	var status HTTPStatusError
	if err == nil || ctx.Err() != nil || !(os.IsTimeout(err) || errors.As(err, &status) && status.Code >= 500 && status.Code < 600) {
		return body, err
	}
	u, parseErr := url.Parse(address)
	if parseErr != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 6 || parts[2] != "releases" || parts[3] != "download" {
		return nil, err
	}
	api := "https://api.github.com/repos/" + parts[0] + "/" + parts[1] + "/releases/"
	metadata, err := fetch(api+"tags/"+url.PathEscape(parts[4]), "application/vnd.github+json", 4<<20)
	if err != nil {
		return nil, fmt.Errorf("release API lookup: %w", err)
	}
	var release struct {
		Assets []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(metadata, &release); err != nil {
		return nil, fmt.Errorf("release API lookup: %w", err)
	}
	for _, asset := range release.Assets {
		if asset.Name == parts[5] && asset.ID > 0 {
			return fetch(api+"assets/"+strconv.FormatInt(asset.ID, 10), "application/octet-stream", maxBytes)
		}
	}
	return nil, fmt.Errorf("release API has no asset %q for tag %s", parts[5], parts[4])
}
