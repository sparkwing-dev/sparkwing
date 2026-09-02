package sourceurl

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
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
	// safety: ESC, VT and NUL survive the whitespace check and the scp-like branch never reaches url.Parse.
	if strings.IndexFunc(raw, isControl) >= 0 {
		return "", fmt.Errorf("repo URL contains a control character")
	}
	// safety: git reads a leading dash as an option rather than a repository.
	if strings.HasPrefix(raw, "-") {
		return "", fmt.Errorf("repo URL must not begin with '-'")
	}
	if match := scpLikeRE.FindStringSubmatch(raw); match != nil {
		if err := validateHost(match[1]); err != nil {
			return "", err
		}
		if strings.TrimSpace(match[2]) == "" {
			return "", fmt.Errorf("repo URL path required")
		}
		// safety: git passes the scp-like path to ssh as its own argv word.
		if strings.HasPrefix(match[2], "-") {
			return "", fmt.Errorf("repo URL path must not begin with '-'")
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
		// safety: the userinfo becomes the leading token of the ssh destination argument.
		if strings.HasPrefix(u.User.Username(), "-") {
			return "", fmt.Errorf("repo URL userinfo must not begin with '-'")
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

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f
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
	// safety: git reads a leading dash as an option rather than a host.
	if strings.HasPrefix(host, "-") {
		return fmt.Errorf("repo URL host must not begin with '-'")
	}
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	// safety: a trailing dot is an equally resolvable spelling of the same name.
	host = strings.TrimRight(host, ".")
	if host == "" {
		return fmt.Errorf("repo URL host required")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || metadataHosts[host] {
		return fmt.Errorf("repo URL host %q is not allowed", host)
	}
	// safety: 127.1, 0x7f000001, and 017700000001 all resolve to loopback, so canonicalize first.
	if ip := parseHostIP(host); ip != nil && !routableIP(ip) {
		return fmt.Errorf("repo URL host %q is not allowed", host)
	}
	return nil
}

var metadataHosts = map[string]bool{
	"metadata":                 true,
	"metadata.google.internal": true,
}

func routableIP(ip net.IP) bool {
	switch {
	case ip.IsLoopback(), ip.IsPrivate(), ip.IsUnspecified(),
		ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast(),
		isCarrierGradeNAT(ip):
		return false
	}
	return true
}

func isCarrierGradeNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64
}

func parseHostIP(host string) net.IP {
	// safety: a zone id defeats ParseIP, and every resolver ignores it when matching the address.
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	return parseNumericIPv4(host)
}

func parseNumericIPv4(host string) net.IP {
	labels := strings.Split(host, ".")
	if len(labels) > 4 {
		return nil
	}
	values := make([]uint64, len(labels))
	for i, label := range labels {
		v, ok := parseNumericLabel(label)
		if !ok {
			return nil
		}
		values[i] = v
	}
	last := len(values) - 1
	var addr uint32
	for i := range last {
		if values[i] > 0xff {
			return nil
		}
		addr |= uint32(values[i]) << (8 * (3 - i))
	}
	tail := uint64(1) << (8 * (4 - last))
	if values[last] >= tail {
		return nil
	}
	addr |= uint32(values[last])
	return net.IPv4(byte(addr>>24), byte(addr>>16), byte(addr>>8), byte(addr))
}

func parseNumericLabel(label string) (uint64, bool) {
	base := 10
	switch {
	case strings.HasPrefix(label, "0x"):
		label, base = label[2:], 16
	case len(label) > 1 && label[0] == '0':
		label, base = label[1:], 8
	}
	if label == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(label, base, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
