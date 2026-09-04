package executorinfo

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"regexp"
	"runtime"
	"strings"
)

var platformValuePattern = regexp.MustCompile(`^[a-z0-9_]{1,24}$`)

var normalizedOS = map[string]string{
	"aix": "aix", "android": "android", "darwin": "macos", "dragonfly": "dragonfly",
	"freebsd": "freebsd", "illumos": "illumos", "ios": "ios", "js": "js",
	"linux": "linux", "macos": "macos", "netbsd": "netbsd", "openbsd": "openbsd",
	"plan9": "plan9", "solaris": "solaris", "wasip1": "wasip1", "windows": "windows",
}

var normalizedArch = map[string]bool{
	"386": true, "amd64": true, "arm": true, "arm64": true, "loong64": true,
	"mips": true, "mips64": true, "mips64le": true, "mipsle": true,
	"ppc64": true, "ppc64le": true, "riscv64": true, "s390x": true, "wasm": true,
}

// ObservedPlatform contains machine facts detected by the helper itself. It
// stays separate from controller-owned capabilities and placement policy.
type ObservedPlatform struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Environment string `json:"environment"`
	HostOS      string `json:"host_os,omitempty"`
}

// DetectObservedPlatform reports bounded runtime facts without reading host
// names, user names, distro names, network peers, or credentials.
func DetectObservedPlatform() ObservedPlatform {
	return detectObservedPlatform(runtime.GOOS, runtime.GOARCH, os.LookupEnv, os.ReadFile)
}

func detectObservedPlatform(goos, goarch string, lookupEnv func(string) (string, bool), readFile func(string) ([]byte, error)) ObservedPlatform {
	observed := ObservedPlatform{
		OS: normalizeOS(goos), Arch: normalizeArch(goarch), Environment: "native",
	}
	if strings.EqualFold(goos, "linux") && detectsWSL(lookupEnv, readFile) {
		observed.Environment = "wsl"
		observed.HostOS = "windows"
	}
	return observed
}

func detectsWSL(lookupEnv func(string) (string, bool), readFile func(string) ([]byte, error)) bool {
	for _, key := range []string{"WSL_INTEROP", "WSL_DISTRO_NAME"} {
		if value, ok := lookupEnv(key); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	for _, path := range []string{"/proc/sys/kernel/osrelease", "/proc/version"} {
		body, err := readFile(path)
		if err == nil {
			value := strings.ToLower(string(body))
			if strings.Contains(value, "microsoft") || strings.Contains(value, "wsl") {
				return true
			}
		}
	}
	return false
}

func normalizeOS(value string) string {
	if normalized, ok := normalizedOS[strings.ToLower(strings.TrimSpace(value))]; ok {
		return normalized
	}
	return "unknown"
}

func normalizeArch(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if normalizedArch[value] {
		return value
	}
	return "unknown"
}

// Validate rejects malformed or contradictory observations before they cross
// a heartbeat boundary.
func (p ObservedPlatform) Validate() error {
	if (!platformValuePattern.MatchString(p.OS) || (p.OS != "unknown" && normalizedOS[p.OS] != p.OS)) ||
		(!platformValuePattern.MatchString(p.Arch) || (p.Arch != "unknown" && !normalizedArch[p.Arch])) {
		return errors.New("observed platform requires bounded normalized os and arch")
	}
	switch p.Environment {
	case "native":
		if p.HostOS != "" {
			return errors.New("native observed platform cannot report host_os")
		}
	case "wsl":
		if p.OS != "linux" || p.HostOS != "windows" {
			return errors.New("wsl observed platform must report os linux and host_os windows")
		}
	default:
		return errors.New("observed platform environment must be native or wsl")
	}
	return nil
}

// MarshalJSON refuses to put unbounded or contradictory self-observations on
// a wire before the controller can validate them again.
func (p ObservedPlatform) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type plain ObservedPlatform
	return json.Marshal(plain(p))
}

// LogValue keeps structured output limited to the four bounded public facts.
func (p ObservedPlatform) LogValue() slog.Value {
	if err := p.Validate(); err != nil {
		return slog.GroupValue(slog.String("status", "invalid"))
	}
	attrs := []slog.Attr{
		slog.String("os", p.OS), slog.String("arch", p.Arch),
		slog.String("environment", p.Environment),
	}
	if p.HostOS != "" {
		attrs = append(attrs, slog.String("host_os", p.HostOS))
	}
	return slog.GroupValue(attrs...)
}
