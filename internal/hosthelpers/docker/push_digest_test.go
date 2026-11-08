package docker

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestPushedManifestDigestRejectsOtherContent(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	footer := "v1.2: digest: " + digest + " size: 123\n"
	for _, tc := range []struct{ name, output, want string }{
		{"manifest", "layer: pushed\n" + footer, digest},
		{"windows-newlines", strings.ReplaceAll(footer, "\n", "\r\n"), digest},
		{"same-repeated", footer + footer, digest},
		{"conflicting", footer + strings.ReplaceAll(footer, "aaaa", "bbbb"), ""},
		{"other-tag", strings.ReplaceAll(footer, "v1.2", "v1.2-evil"), ""},
		{"configuration-id", "sha256:" + strings.Repeat("a", 64), ""},
		{"malformed", "v1.2: digest: sha256:short size: 123\n", ""},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pushedManifestDigest(tc.output, "v1.2"); got != tc.want {
				t.Fatalf("digest = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildAndPushMissingMetadataKeepsPushSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("existing fake Docker helper uses a POSIX shell")
	}
	fakeBuildxInspectDocker(t, "Platforms: linux/amd64\n")
	result, err := BuildAndPush(context.Background(), BuildConfig{
		Image: "app", Tags: []string{"tag"}, Registries: []string{"registry"}, Platforms: []string{"linux/amd64"},
	})
	if err != nil || len(result.Registries) != 1 || len(result.Digests) != 0 {
		t.Fatalf("successful push without metadata = %+v, %v", result, err)
	}
}
