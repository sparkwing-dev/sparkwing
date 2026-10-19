package runspec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// CanonicalRepository treats resolved GitHub IDs as identity, never authority.
func CanonicalRepository(in RepoInput) (Repository, error) {
	display := safeDisplay(in.Display)
	if in.GitHubRepositoryID.Set {
		if in.GitHubRepositoryID.Value <= 0 {
			return Repository{}, errors.New("repository ID must be positive")
		}
		if display == "" {
			display = safeDisplay(in.RemoteURL)
		}
		key := RepoKey("gh:" + strconv.FormatInt(in.GitHubRepositoryID.Value, 10))
		if display == "" {
			display = string(key)
		}
		return Repository{Key: key, Display: display}, nil
	}
	remote := in.RemoteURL
	if remote == "" {
		return localRepository(in.AbsolutePath, display)
	}
	if filepath.IsAbs(remote) {
		return localRepository(remote, display)
	}
	if !strings.Contains(remote, "://") {
		if i := strings.IndexByte(remote, ':'); i > 0 {
			remote = "ssh://" + remote[:i] + "/" + remote[i+1:]
		} else {
			return Repository{}, errors.New("repository remote must be a URL or absolute filesystem path")
		}
	}
	u, err := url.Parse(remote)
	if err != nil {
		return Repository{}, errors.New("invalid repository remote")
	}
	if u.Scheme == "file" {
		if u.Host != "" && u.Host != "localhost" {
			return Repository{}, errors.New("filesystem repository must be local")
		}
		return localRepository(u.Path, display)
	}
	if u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git" {
		return Repository{}, errors.New("unsupported repository remote scheme")
	}
	if strings.EqualFold(u.Hostname(), "github.com") {
		return Repository{}, errors.New("GitHub repository requires a resolved numeric ID")
	}
	host := strings.ToLower(u.Host)
	path := strings.TrimSuffix(strings.TrimRight(strings.TrimLeft(u.Path, "/"), "/"), ".git")
	if host == "" || path == "" || strings.ContainsAny(host+path, "\x00\r\n") {
		return Repository{}, errors.New("repository remote requires host and path")
	}
	for _, provider := range caseInsensitiveProviders {
		if strings.EqualFold(u.Hostname(), provider) {
			path = strings.ToLower(path)
		}
	}
	key := RepoKey("git:" + host + "/" + path)
	if display == "" {
		display = host + "/" + path
	}
	return Repository{Key: key, Display: display}, nil
}

func localRepository(path, display string) (Repository, error) {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return Repository{}, errors.New("filesystem repository requires an absolute path")
	}
	path = filepath.Clean(path)
	sum := sha256.Sum256([]byte(path))
	if display == "" {
		display = path
	}
	return Repository{Key: RepoKey("local:" + hex.EncodeToString(sum[:])), Display: display}, nil
}

func safeDisplay(s string) string {
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return ""
		}
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return u.String()
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// ValidateRepoUse keeps machine-scoped identities out of controller namespaces.
func ValidateRepoUse(key RepoKey, use RepoUse) error {
	s := string(key)
	switch use {
	case RepoMachineStore, RepoAccessRule, RepoControllerMemo:
	default:
		return errors.New("unknown repository use")
	}
	switch {
	case strings.HasPrefix(s, "local:"):
		h := strings.TrimPrefix(s, "local:")
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != 32 || h != strings.ToLower(h) {
			return errors.New("invalid local repository key")
		}
		if use != RepoMachineStore {
			return errors.New("local repository key is restricted to its machine store")
		}
	case strings.HasPrefix(s, "gh:"):
		id, err := strconv.ParseInt(s[3:], 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != s[3:] {
			return errors.New("invalid GitHub repository key")
		}
	case strings.HasPrefix(s, "git:"):
		got, err := CanonicalRepository(RepoInput{RemoteURL: "ssh://" + s[4:]})
		if err != nil || got.Key != key {
			return errors.New("invalid git repository key")
		}
	default:
		return errors.New("invalid repository key")
	}
	return nil
}
