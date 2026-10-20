package source

import (
	"bufio"
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

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func seedBundle(ctx context.Context, client *gitClient, checkout, digest, rawURL string, prerequisites func([]string) error) (err error) {
	expected, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(expected) != sha256.Size {
		return errors.New("source bundle digest must be SHA-256")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return errors.New("source bundle requires an authorized HTTP(S) URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return errors.New("source bundle URL is invalid")
	}
	httpClient := &http.Client{Transport: client.bundleTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return errors.New("source bundle download failed")
	}
	defer func() {
		if response.Body.Close() != nil {
			// safety: transport errors can contain the signed URL, including its credential.
			err = errors.Join(err, errors.New("source bundle download close failed"))
		}
	}()
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
	if copyErr != nil {
		if ctx.Err() != nil {
			copyErr = context.Cause(ctx)
		} else {
			copyErr = errors.New("source bundle transfer failed")
		}
	}
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expected)) {
		return errors.New("source bundle digest mismatch")
	}
	return unbundle(ctx, client, checkout, name, prerequisites)
}

// safety: a working-tree bundle stops at commits its remote already has, so
// those must arrive from the remote before the bundle can apply.
func unbundle(ctx context.Context, client *gitClient, checkout, name string, prerequisites func([]string) error) error {
	missing, err := bundlePrerequisites(name)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		if err := prerequisites(missing); err != nil {
			return err
		}
	}
	_, err = client.run(ctx, checkout, "", "bundle", "unbundle", name)
	return err
}

func bundlePrerequisites(name string) ([]string, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	lines := bufio.NewReader(io.LimitReader(file, 1<<20))
	var out []string
	for first := true; ; first = false {
		line, err := lines.ReadString('\n')
		if err != nil {
			return nil, errors.New("source bundle header is malformed")
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case first && line != "# v2 git bundle" && line != "# v3 git bundle":
			return nil, errors.New("source bundle version is unsupported")
		case line == "":
			return out, nil
		case strings.HasPrefix(line, "-"):
			sha, _, _ := strings.Cut(line[1:], " ")
			if !runspec.IsCommitID(sha) {
				return nil, errors.New("source bundle prerequisite is malformed")
			}
			out = append(out, sha)
		}
	}
}
