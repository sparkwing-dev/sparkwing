package sourceurl

import (
	"errors"
	"net/url"
	"strings"
)

// remoteParts splits a validated clone URL into its lowercased hostname (no
// port), its ssh port if it names one, and its path with the case kept and
// no leading or trailing slash.
func remoteParts(raw string) (host, port, path string, err error) {
	validated, err := ValidateCloneURL(raw)
	if err != nil {
		return "", "", "", err
	}
	if match := scpLikeRE.FindStringSubmatch(validated); match != nil {
		host, path = match[1], match[2]
	} else {
		u, perr := url.Parse(validated)
		if perr != nil {
			return "", "", "", perr
		}
		host, port, path = u.Hostname(), u.Port(), u.Path
	}
	host = strings.TrimRight(strings.ToLower(host), ".")
	path = strings.Trim(path, "/")
	if host == "" || path == "" {
		return "", "", "", errors.New("repo URL names no host and path")
	}
	return host, port, path, nil
}

// Host is the lowercased hostname a clone URL names, without a port: the
// host a team git credential is bound to.
func Host(raw string) (string, error) {
	host, _, _, err := remoteParts(raw)
	return host, err
}

// HTTPSForm is raw as an https clone URL of the same host and path, so an
// ssh remote can be fetched with an https credential. An ssh port is dropped,
// since it names the ssh daemon.
func HTTPSForm(raw string) (string, error) {
	host, _, path, err := remoteParts(raw)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(raw, "https://") {
		return ValidateCloneURL(raw)
	}
	return ValidateCloneURL("https://" + host + "/" + path)
}

// SSHForm is raw as an ssh clone URL of the same host and path for the git
// user every forge serves deploy keys under, so an https remote can be
// fetched with a deploy key. An ssh or scp-like remote is kept as written.
func SSHForm(raw string) (string, error) {
	host, _, path, err := remoteParts(raw)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(raw, "https://") {
		return ValidateCloneURL(raw)
	}
	return ValidateCloneURL("ssh://git@" + host + "/" + path)
}
