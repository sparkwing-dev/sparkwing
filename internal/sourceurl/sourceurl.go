package sourceurl

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
)

var scpLikeRE = regexp.MustCompile(`^[A-Za-z0-9._-]+@([^:]+):(.+)$`)

var hostnameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

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
		// safety: ssh splits the destination at the last '@', so a capture that still holds
		// one names a different machine than the guard below inspects.
		if !hostnameRE.MatchString(match[1]) {
			return "", fmt.Errorf("repo URL host %q is not a hostname", match[1])
		}
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
	if isInternalName(host) {
		return fmt.Errorf("repo URL host %q is not allowed", host)
	}
	// safety: 127.1, 0x7f000001, and 017700000001 all resolve to loopback, so canonicalize first.
	if ip := parseHostIP(host); ip != nil && !routableIP(ip) {
		return fmt.Errorf("repo URL host %q is not allowed", host)
	}
	if pol := hostPolicy.Load(); pol != nil {
		return (*pol)(host)
	}
	return nil
}

// HostPolicy is a deployment's own answer to whether a clone may go to host. It
// sees the lowercased host with any trailing dot removed, only after the
// built-in checks have passed, and its error reaches the caller unchanged.
type HostPolicy func(host string) error

var hostPolicy atomic.Pointer[HostPolicy]

// SetHostPolicy installs pol for every clone URL this process validates from
// here on, and a nil pol leaves only the built-in checks. It exists because a
// name is never resolved during validation: git resolves it again when it
// connects, so an address checked here says nothing about the address reached
// then, and a deployment that must bound where clones go states that boundary
// by name -- an allowlist of forges it clones from, or a denylist of names it
// knows point inward -- rather than by an address that will not hold still.
func SetHostPolicy(pol HostPolicy) {
	if pol == nil {
		hostPolicy.Store(nil)
		return
	}
	hostPolicy.Store(&pol)
}

// safety: no name in these ever denotes a host outside the local network or the
// instance itself, whatever it happens to resolve to on the day git dials it.
var internalHostNames = map[string]bool{
	"localhost":                true,
	"local":                    true,
	"internal":                 true,
	"localdomain":              true,
	"home.arpa":                true,
	"metadata":                 true,
	"metadata.google.internal": true,
	// safety: every Debian and Ubuntu /etc/hosts ships this stanza, so these
	// names reach loopback, link-local, or multicast without any resolver.
	"ip6-localhost":   true,
	"ip6-loopback":    true,
	"ip6-localnet":    true,
	"ip6-mcastprefix": true,
	"ip6-allnodes":    true,
	"ip6-allrouters":  true,
}

var internalHostSuffixes = []string{".localhost", ".local", ".internal", ".localdomain", ".home.arpa"}

func isInternalName(host string) bool {
	if internalHostNames[host] {
		return true
	}
	for _, suffix := range internalHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
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
