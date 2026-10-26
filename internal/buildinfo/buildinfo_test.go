package buildinfo

import (
	"runtime"
	"testing"
)

func TestReadPrefersStampedVersionAndReportsPlatform(t *testing.T) {
	identity := Read("sparkwing-runner", "v9.9.9")
	if identity.Binary != "sparkwing-runner" || identity.Version != "v9.9.9" {
		t.Fatalf("Read() = %+v", identity)
	}
	if identity.GOOS != runtime.GOOS || identity.GOARCH != runtime.GOARCH {
		t.Fatalf("platform = %s/%s, want %s/%s", identity.GOOS, identity.GOARCH, runtime.GOOS, runtime.GOARCH)
	}
}

func TestIsReleaseVersion(t *testing.T) {
	for _, version := range []string{"v0.40.0", "v1.2.3"} {
		if !IsReleaseVersion(version) {
			t.Errorf("IsReleaseVersion(%q) = false", version)
		}
	}
	for _, version := range []string{"", "v1.2", "v1.2.3-rc1", "v1.2.3+build", "(devel)"} {
		if IsReleaseVersion(version) {
			t.Errorf("IsReleaseVersion(%q) = true", version)
		}
	}
}
