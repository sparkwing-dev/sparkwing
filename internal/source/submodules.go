package source

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func checkoutSubmodules(ctx context.Context, client *gitClient, checkout, remote string) error {
	file := filepath.Join(checkout, ".gitmodules")
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("source .gitmodules must be a regular file")
	}
	values, err := client.run(ctx, checkout, "", "config", "--null", "--file", file, "--get-regexp", `^submodule\..*\.path$`)
	if gitExit(err, 1) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, row := range strings.Split(strings.TrimSuffix(values, "\x00"), "\x00") {
		key, relative, ok := strings.Cut(row, "\n")
		if !ok || !filepath.IsLocal(relative) || relative == "." {
			return errors.New("source submodule path is invalid")
		}
		submoduleURL, err := client.run(ctx, checkout, "", "config", "--file", file, "--get", strings.TrimSuffix(key, ".path")+".url")
		if err != nil {
			return err
		}
		submoduleURL, err = resolveSubmoduleURL(remote, submoduleURL)
		if err != nil {
			return err
		}
		if err := authorizedSubmodule(remote, submoduleURL, client.credential); err != nil {
			return err
		}
		submoduleURL, err = credentialRemote(submoduleURL, client.credential)
		if err != nil {
			return err
		}
		row, err := client.run(ctx, checkout, "", "ls-tree", "HEAD", "--", relative)
		if err != nil {
			return err
		}
		fields := strings.Fields(row)
		if len(fields) < 3 || fields[0] != "160000" || fields[1] != "commit" || !commitID.MatchString(fields[2]) {
			return errors.New("source submodule is not a sealed gitlink")
		}
		destination := filepath.Join(checkout, relative)
		if err := makeSubmodulePath(checkout, relative); err != nil {
			return err
		}
		if err := cloneCommit(ctx, client, Spec{SHA: fields[2]}, submoduleURL, destination, "", nil); err != nil {
			return err
		}
		if err := checkoutSubmodules(ctx, client, destination, submoduleURL); err != nil {
			return err
		}
	}
	return nil
}

func resolveSubmoduleURL(parent, child string) (string, error) {
	if !strings.HasPrefix(child, "./") && !strings.HasPrefix(child, "../") {
		return sourceurl.ValidateCloneURL(child)
	}
	if strings.Contains(parent, "://") {
		parsed, err := url.Parse(parent)
		if err != nil {
			return "", err
		}
		parsed.Path = path.Join(parsed.Path, child)
		return sourceurl.ValidateCloneURL(parsed.String())
	}
	host, repository, ok := strings.Cut(parent, ":")
	if !ok {
		return "", errors.New("source submodule parent has no remote host")
	}
	return sourceurl.ValidateCloneURL(host + ":" + path.Join(repository, child))
}

func makeSubmodulePath(checkout, relative string) error {
	current := checkout
	for _, part := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("source submodule path crosses a file or symlink")
		}
	}
	entries, err := os.ReadDir(current)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("source submodule directory is not empty")
	}
	return nil
}

func checkoutLFS(ctx context.Context, client *gitClient, checkout, remote, sha string) error {
	endpoint := remote
	if strings.HasPrefix(remote, "https://") {
		endpoint = strings.TrimRight(remote, "/")
		if !strings.HasSuffix(endpoint, ".git") {
			endpoint += ".git"
		}
		endpoint += "/info/lfs"
	}
	// safety: an explicit endpoint overrides repository .lfsconfig without dropping SSH authentication.
	if _, err := client.run(ctx, checkout, endpoint, "-c", "lfs.url="+endpoint, "lfs", "fetch", "origin", sha); err != nil {
		return fmt.Errorf("fetch source LFS: %w", err)
	}
	_, err := client.run(ctx, checkout, "", "lfs", "checkout")
	return err
}
