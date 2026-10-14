package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func seedBundle(ctx context.Context, client *gitClient, checkout, digest string, access Access) (err error) {
	expected, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(expected) != sha256.Size {
		return errors.New("source bundle digest must be SHA-256")
	}
	rawURL, err := access.BundleURL(ctx, digest)
	if err != nil {
		return fmt.Errorf("authorize source bundle: %w", err)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("source bundle requires an authorized HTTPS URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return errors.New("source bundle URL is invalid")
	}
	httpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("source bundle download failed")
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("source bundle download returned HTTP %d", response.StatusCode)
	}
	name := filepath.Join(client.workspace, "seed.bundle")
	file, err := fssecure.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(name)) }()
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), response.Body)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expected)) {
		return errors.New("source bundle digest mismatch")
	}
	_, err = client.run(ctx, checkout, "", "bundle", "unbundle", name)
	return err
}
