package sourceurl

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var scpLikeRE = regexp.MustCompile(`^[A-Za-z0-9._-]+@([^:]+):(.+)$`)

func ValidateCloneURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("repo URL required")
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return "", fmt.Errorf("repo URL contains whitespace")
	}
	if match := scpLikeRE.FindStringSubmatch(raw); match != nil {
		if err := validateHost(match[1]); err != nil {
			return "", err
		}
		if strings.TrimSpace(match[2]) == "" {
			return "", fmt.Errorf("repo URL path required")
		}
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse repo URL: %w", err)
	}
	switch u.Scheme {
	case "https", "ssh":
	default:
		return "", fmt.Errorf("repo URL scheme %q is not allowed", u.Scheme)
	}
	if u.Scheme == "https" && u.User != nil {
		return "", fmt.Errorf("repo URL must not include userinfo")
	}
	if u.Scheme == "ssh" && u.User != nil {
		if _, ok := u.User.Password(); ok {
			return "", fmt.Errorf("repo URL must not include userinfo password")
		}
	}
	if err := validateHost(u.Hostname()); err != nil {
		return "", err
	}
	if strings.Trim(u.Path, "/") == "" {
		return "", fmt.Errorf("repo URL path required")
	}
	return raw, nil
}

func Redact(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if match := scpLikeRE.FindStringSubmatch(raw); match != nil {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("redacted")
	return u.String()
}

func validateHost(host string) error {
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "" {
		return fmt.Errorf("repo URL host required")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("repo URL host %q is not allowed", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("repo URL host %q is not allowed", host)
		}
	}
	return nil
}
