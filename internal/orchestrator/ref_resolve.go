package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

// ResolveRefCommit turns ref into a commit id, fetching first so a ref this
// checkout has never seen still resolves. A ref the working tree already knows
// wins, so a submission runs the commit the operator can see.
func ResolveRefCommit(ctx context.Context, originRepo, ref string, logger *slog.Logger) (string, error) {
	// safety: git reads a leading dash as an option, so a ref like
	// --upload-pack=CMD would run CMD; -- alone does not stop every git verb.
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("ref %q starts with a dash, which git reads as an option rather than a ref", ref)
	}
	out, fetchErr := exec.CommandContext(ctx, "git", "-C", originRepo,
		"fetch", "--quiet", "origin", "--", ref).CombinedOutput()
	logBestEffortGit(ctx, logger, slog.LevelDebug, "fetch", out, fetchErr)

	candidates := []string{ref + "^{commit}"}
	if fetchErr == nil {
		candidates = append(candidates, "FETCH_HEAD^{commit}")
	}
	for _, candidate := range candidates {
		rev, err := exec.CommandContext(ctx, "git", "-C", originRepo,
			"rev-parse", "--verify", "--end-of-options", candidate).Output()
		if err == nil {
			return strings.TrimSpace(string(rev)), nil
		}
	}
	return "", fmt.Errorf("ref %s names no commit in this checkout, and fetching it from origin produced none", ref)
}

func logBestEffortGit(ctx context.Context, logger *slog.Logger, level slog.Level, step string, out []byte, err error) {
	if err == nil || logger == nil {
		return
	}
	logger.Log(ctx, level, "ref worktree git step did not apply",
		"step", step, "error", err, "output", strings.TrimSpace(string(out)))
}
