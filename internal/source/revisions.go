package source

import (
	"cmp"
	"context"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

// ResolveRevisions separates ref intent from the execution and pipeline commits.
func ResolveRevisions(ctx context.Context, git runspec.Git, code runspec.Source, override string, lookup func(string) (string, error)) (runspec.Git, runspec.Source, error) {
	resolve := func(ref string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if commitID.MatchString(ref) {
			return strings.ToLower(ref), nil
		}
		sha, err := lookup(ref)
		if err == nil && !commitID.MatchString(sha) {
			err = &apierr.Error{Code: apierr.InvalidArgs, Message: "source revision did not resolve to a commit"}
		}
		return strings.ToLower(sha), err
	}
	ref := cmp.Or(git.SHA, git.Ref, "HEAD")
	var err error
	git.SHA, err = resolve(ref)
	if err != nil {
		return git, code, err
	}
	override = strings.TrimSpace(override)
	intent := cmp.Or(strings.TrimSpace(code.RequestedRef), override)
	code.RequestedRef, code.Ref = intent, git.SHA
	if intent != "" {
		code.Ref, err = resolve(intent)
		if err != nil {
			return git, code, err
		}
	}
	if override != "" && override != intent {
		selected, err := resolve(override)
		if err != nil {
			return git, code, err
		}
		if selected != code.Ref {
			return git, code, &apierr.Error{Code: apierr.InvalidArgs, Message: "pipeline revision disagrees with the configured source pin"}
		}
	}
	return git, code, ctx.Err()
}
