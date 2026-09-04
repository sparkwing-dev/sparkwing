package executorinfo

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestDetectObservedPlatformNormalizesNativeAndWSL(t *testing.T) {
	readMissing := func(string) ([]byte, error) { return nil, errors.New("missing") }
	lookupMissing := func(string) (string, bool) { return "", false }
	for _, tc := range []struct {
		name, goos, goarch string
		lookup             func(string) (string, bool)
		read               func(string) ([]byte, error)
		want               ObservedPlatform
	}{
		{name: "native windows", goos: "windows", goarch: "amd64", lookup: lookupMissing, read: readMissing, want: ObservedPlatform{OS: "windows", Arch: "amd64", Environment: "native"}},
		{name: "native mac", goos: "darwin", goarch: "arm64", lookup: lookupMissing, read: readMissing, want: ObservedPlatform{OS: "macos", Arch: "arm64", Environment: "native"}},
		{name: "unsupported runtime", goos: "future-os", goarch: "future-arch", lookup: lookupMissing, read: readMissing, want: ObservedPlatform{OS: "unknown", Arch: "unknown", Environment: "native"}},
		{name: "wsl environment", goos: "linux", goarch: "amd64", lookup: func(key string) (string, bool) {
			return map[string]string{"WSL_DISTRO_NAME": "private-distro-name"}[key], key == "WSL_DISTRO_NAME"
		}, read: readMissing, want: ObservedPlatform{OS: "linux", Arch: "amd64", Environment: "wsl", HostOS: "windows"}},
		{name: "wsl kernel", goos: "linux", goarch: "arm64", lookup: lookupMissing, read: func(path string) ([]byte, error) {
			if path == "/proc/sys/kernel/osrelease" {
				return []byte("6.6.87.2-microsoft-standard-WSL2"), nil
			}
			return nil, errors.New("missing")
		}, want: ObservedPlatform{OS: "linux", Arch: "arm64", Environment: "wsl", HostOS: "windows"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := detectObservedPlatform(tc.goos, tc.goarch, tc.lookup, tc.read)
			if got != tc.want {
				t.Fatalf("observed platform = %+v, want %+v", got, tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("validate detected platform: %v", err)
			}
		})
	}
}

func TestObservedPlatformOutputCannotExposeDetectionInputsOrTrustLabels(t *testing.T) {
	const privateDistro = "private-work-distro"
	observed := detectObservedPlatform("linux", "amd64", func(key string) (string, bool) {
		if key == "WSL_DISTRO_NAME" {
			return privateDistro, true
		}
		return "", false
	}, func(string) ([]byte, error) { return nil, errors.New("missing") })
	payload, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	logger.Info("platform", "observed_platform", observed)
	combined := string(payload) + logs.String()
	for _, forbidden := range []string{privateDistro, "capabilities", "location", "placement", "token", "principal", "hostname"} {
		if strings.Contains(strings.ToLower(combined), forbidden) {
			t.Errorf("observed platform output exposes %q: %s", forbidden, combined)
		}
	}
}

func TestObservedPlatformValidationRejectsContradictoryOrUnboundedFacts(t *testing.T) {
	for _, observed := range []ObservedPlatform{
		{OS: "linux", Arch: "amd64", Environment: "native", HostOS: "windows"},
		{OS: "windows", Arch: "amd64", Environment: "wsl", HostOS: "windows"},
		{OS: "linux", Arch: "amd64", Environment: "wsl"},
		{OS: "linux", Arch: strings.Repeat("a", 25), Environment: "native"},
		{OS: "private-os", Arch: "amd64", Environment: "native"},
		{OS: "linux", Arch: "private-arch", Environment: "native"},
		{OS: "linux", Arch: "amd64", Environment: "container"},
	} {
		if err := observed.Validate(); err == nil {
			t.Errorf("Validate(%+v) succeeded", observed)
		}
		if payload, err := json.Marshal(observed); err == nil || strings.Contains(string(payload), observed.Arch) {
			t.Errorf("MarshalJSON(%+v) = %q, %v", observed, payload, err)
		}
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("platform", "observed_platform", ObservedPlatform{
		OS: "linux", Arch: "private-credential-value", Environment: "native",
	})
	if strings.Contains(logs.String(), "private-credential-value") || !strings.Contains(logs.String(), `"status":"invalid"`) {
		t.Fatalf("invalid platform log = %s", logs.String())
	}
}
