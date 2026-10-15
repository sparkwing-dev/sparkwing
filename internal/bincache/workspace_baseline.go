package bincache

import (
	"fmt"
	"regexp"
	"strings"
)

// WorkspaceBaseline records the snapshot's merge base and remote-tracking ref.
type WorkspaceBaseline struct {
	Ref string
	SHA string
}

var gitObjectRE = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

func validateGitObject(sha string) (string, error) {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if !gitObjectRE.MatchString(sha) {
		return "", fmt.Errorf("git sha must be a 40-64 character hex object id")
	}
	return sha, nil
}

// Resolve validates and normalizes the snapshot baseline.
func (b WorkspaceBaseline) Resolve() (WorkspaceBaseline, error) {
	sha, err := validateGitObject(b.SHA)
	if err != nil {
		return WorkspaceBaseline{}, fmt.Errorf("working-tree baseline: %w", err)
	}
	ref := strings.TrimSpace(b.Ref)
	if err := validateRemoteTrackingRef(ref); err != nil {
		return WorkspaceBaseline{}, err
	}
	return WorkspaceBaseline{Ref: ref, SHA: sha}, nil
}

// safety: this name becomes a ref path and a git argument, so only a plain remote/branch passes.
var remoteTrackingRefRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)+$`)

func validateRemoteTrackingRef(ref string) error {
	if !remoteTrackingRefRE.MatchString(ref) || strings.Contains(ref, "..") {
		return fmt.Errorf("working-tree baseline: ref %q must name a remote-tracking branch such as origin/main", ref)
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("working-tree baseline: ref %q must name a remote-tracking branch such as origin/main", ref)
		}
	}
	return nil
}
