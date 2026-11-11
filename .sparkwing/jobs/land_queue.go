package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
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
	tree, err := git(ctx, "rev-parse", head.Commit+"^{tree}")
	if err != nil {
		return target, fmt.Errorf("land queue: resolve the gated commit's tree: %w", err)
	}
	if tree != head.Tree {
		return target, errors.New("land queue: pushed commit does not match the gated tree; enqueue again")
	}
	if _, err := git(ctx, "merge-base", "--is-ancestor", head.Base, head.Commit); err != nil {
		return target, fmt.Errorf("land queue: gated commit does not fast-forward planned main %s: %w", short(head.Base), err)
	}
	ref := "refs/heads/" + landBaseBranch
	refspec := head.Commit + ":" + ref
	// safety: the lease makes the base comparison atomic; ancestry prevents a forced history update.
	out, err := git(ctx, "push", "--porcelain", "--no-verify", "--force-with-lease="+ref+":"+head.Base, landBaseRemote, refspec)
	if err != nil {
		return target, fmt.Errorf("land queue: atomic push from planned main %s was refused; enqueue again through main's bin/queue-land.sh: %w", short(head.Base), err)
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 || fields[1] != refspec {
			continue
		}
		if fields[0] == "=" {
			return target, errors.New("land queue: origin/main is already at the gated commit; no atomic update occurred; enqueue again through main's bin/queue-land.sh")
		}
		if fields[0] == " " {
			sparkwing.Info(ctx, "%s fast-forwarded from planned %s to %s", landBaseRef, short(head.Base), short(head.Commit))
			return target, nil
		}
	}
	return target, errors.New("land queue: push did not confirm a fast-forward update of main; inspect origin/main and enqueue again")
}
