package source

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func seedBundle(ctx context.Context, client *gitClient, checkout, digest, rawURL string, prerequisites func([]string) error) (err error) {
	expected, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || len(expected) != sha256.Size {
		return errors.New("source bundle digest must be SHA-256")
	}
	body, err := (blob.TransferClient{Transport: client.bundleTransport}).Transfer(ctx, blob.SignedTransfer{URL: rawURL}, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, body.Close()) }()
	name := filepath.Join(client.workspace, "seed.bundle")
	file, err := fssecure.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.Remove(name)) }()
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), body)
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
