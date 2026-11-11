package jobs

import (
	"context"
	"errors"
	"fmt"
)

func prepareQueuedLanding(ctx context.Context, git gitRunner, dir, submitted, main string) error {
	branch, err := git(ctx, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || branch == "" || branch == landBaseBranch {
		return fmt.Errorf("land queue: use a work branch in %s", dir)
	}
	if err := requireClean(ctx, git, dir); err != nil {
		return err
	}
	head, err := git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if submitted == "" || head != submitted {
		return errors.New("land queue: HEAD changed since submission; rerun focused tests and enqueue again")
	}
	if err := requireQueuedMain(ctx, git, dir, main); err != nil {
		return err
	}
	if _, err := git(ctx, "merge", "--no-edit", landBaseRef); err != nil {
		_, abortErr := git(ctx, "merge", "--abort")
		return fmt.Errorf("land queue: merge failed; resolve with git -C %s merge %s and enqueue again: %w", dir, landBaseRef, errors.Join(err, abortErr))
	}
	return requireClean(ctx, git, dir)
}

func requireQueuedMain(ctx context.Context, git gitRunner, dir, main string) error {
	if err := fetchLandBase(ctx, git, dir); err != nil {
		return err
	}
	base, err := git(ctx, "rev-parse", landBaseRef)
	if err != nil {
		return err
	}
	if main == "" || base != main {
		return queuedMainMoved(main, base)
	}
	return nil
}

func queuedMainMoved(planned, current string) error {
	return fmt.Errorf("land queue: origin/main changed from planned %s to %s; enqueue again through main's bin/queue-land.sh to compile the current gate", short(planned), short(current))
}

func pushQueuedLanding(ctx context.Context, git gitRunner, dir string, head landHead) (landTarget, error) {
	target := landTarget{Commit: head.Commit, Tree: head.Tree, Base: head.Base}
	if err := verifyLandingCheckout(ctx, git, dir, head); err != nil {
		return target, err
	}
	if err := requireQueuedMain(ctx, git, dir, head.Base); err != nil {
		return target, err
	}
	if err := pushLanding(ctx, git, dir, head.Commit, head.Tree); err != nil {
		return target, err
	}
	return target, nil
}
