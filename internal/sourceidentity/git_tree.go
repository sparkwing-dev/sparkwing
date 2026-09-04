package sourceidentity

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os/exec"
	"strings"
)

// GitTreeManifestDigest hashes Git's canonical recursive tree listing. The
// digest binds paths, modes, object kinds, and full object identities.
func GitTreeManifestDigest(ctx context.Context, repo, revision string) (string, error) {
	if repo == "" || revision == "" {
		return "", fmt.Errorf("git tree manifest requires a repository and revision")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "ls-tree", "-rz", "--full-tree", revision+"^{tree}")
	raw, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read Git tree manifest: %w", err)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("git tree manifest is empty")
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

// IsSHA256 reports whether value is a canonical lowercase SHA-256 digest.
func IsSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
