package bincache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/discovery"
)

type signedDownload struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// TryBinaryPreferSigned fetches a binary through the announced signing route.
// An older controller without that route retains the direct cache path.
func TryBinaryPreferSigned(ctx context.Context, controllerURL, controllerToken, cacheGrant, legacyURL, hash, dest string) error {
	if controllerURL == "" {
		return TryBinary(ctx, legacyURL, cacheGrant, hash, dest)
	}
	services, err := discovery.ServicesFor(ctx, controllerURL, controllerToken)
	if err != nil {
		if legacyURL != "" {
			return TryBinary(ctx, legacyURL, cacheGrant, hash, dest)
		}
		return err
	}
	if services.DataDownloadURL == "" {
		if legacyURL == "" {
			return ErrMiss
		}
		return TryBinary(ctx, legacyURL, cacheGrant, hash, dest)
	}
	endpoint, err := url.Parse(strings.TrimRight(controllerURL, "/") + "/")
	if err != nil {
		return err
	}
	ref, err := url.Parse(services.DataDownloadURL)
	if err != nil {
		return err
	}
	if ref.Scheme != "" || ref.Host != "" || !strings.HasPrefix(ref.Path, "/") {
		return fmt.Errorf("invalid download signing route")
	}
	endpoint = endpoint.ResolveReference(ref)
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return fmt.Errorf("invalid download signing URL")
	}
	body, err := json.Marshal(struct {
		Kind string `json:"kind"`
		Key  string `json:"key"`
	}{Kind: "binary", Key: "bins/" + hash})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cacheGrant != "" {
		req.Header.Set("Authorization", "Bearer "+cacheGrant)
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrMiss
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sign binary download: %s", resp.Status)
	}
	var signed signedDownload
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&signed); err != nil {
		return err
	}
	want, err := hex.DecodeString(signed.SHA256)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("%w: invalid signed digest", ErrDigest)
	}
	if signed.Size < 0 {
		return fmt.Errorf("invalid signed download size")
	}
	blobURL, err := url.Parse(signed.URL)
	if err != nil || (blobURL.Scheme != "http" && blobURL.Scheme != "https") || blobURL.Host == "" {
		return fmt.Errorf("invalid signed download URL")
	}
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, blobURL.String(), nil)
	if err != nil {
		return err
	}
	blob, err := client.Do(get)
	if err != nil {
		return err
	}
	defer blob.Body.Close()
	if blob.StatusCode != http.StatusOK {
		return fmt.Errorf("signed binary GET: %s", blob.Status)
	}
	if err := mkdirCache(filepath.Dir(dest)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dest), ".fetch-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	sum := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, sum), io.LimitReader(blob.Body, signed.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != signed.Size || !bytes.Equal(sum.Sum(nil), want) {
		return fmt.Errorf("%w: bin/%s", ErrDigest, hash)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	return nil
}
