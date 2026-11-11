package jobs

import (
	"context"
	"testing"
)

func TestLandQueueRefusesMainMovementAtPush(t *testing.T) {
	for _, timing := range []string{"before-check", "at-push"} {
		for _, target := range []string{"prefix", "gated", "diverged"} {
			t.Run(timing+"/"+target, func(t *testing.T) {
				r := newLandRemote(t)
				prefix := r.git(r.dev, "rev-parse", "HEAD")
				r.commit(r.dev, map[string]string{"extra.txt": "candidate tail\n"}, "candidate tail")
				head := r.head()
				moved := prefix
				if target == "gated" {
					moved = head.Commit
				}
				if target == "diverged" {
					moved = r.commit(r.other, map[string]string{"different.txt": "different gate\n"}, "independent main")
				}
				advance := func() {
					source := r.dev
					if target == "diverged" {
						source = r.other
					}
					r.git(r.bare, "fetch", "--quiet", source, moved+":refs/heads/main")
				}
				if timing == "before-check" {
					advance()
				}
				pushed := false
				git := func(ctx context.Context, args ...string) (string, error) {
					if len(args) > 0 && args[0] == "push" {
						pushed = true
						if timing == "at-push" {
							advance()
						}
					}
					return gitIn(r.dev)(ctx, args...)
				}
				_, err := pushQueuedLanding(newTestContext(t), git, r.dev, head)
				remote := r.remoteMain()
				t.Logf("pinned=%s advanced=%s gated=%s remote=%s push=%v err=%v", head.Base, moved, head.Commit, remote, pushed, err)
				if err == nil {
					t.Fatal("queued push accepted main movement after the trusted pin")
				}
				if remote != moved {
					t.Fatalf("refused push changed remote from %s to %s", moved, remote)
				}
			})
		}
	}
}

func TestLandQueueRefusesNonFastForwardBeforePush(t *testing.T) {
	r := newLandRemote(t)
	base := r.remoteMain()
	r.git(r.dev, "switch", "--orphan", "rewritten")
	commit := r.commit(r.dev, map[string]string{"unrelated.txt": "unrelated history\n"}, "unrelated candidate")
	head := landHead{Branch: "rewritten", Commit: commit, Tree: r.git(r.dev, "rev-parse", "HEAD^{tree}"), Base: base}
	pushed := false
	git := func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "push" {
			pushed = true
		}
		return gitIn(r.dev)(ctx, args...)
	}
	_, err := pushQueuedLanding(newTestContext(t), git, r.dev, head)
	if err == nil || pushed {
		t.Fatalf("non-fast-forward candidate reached push: pushed=%v err=%v", pushed, err)
	}
	if r.remoteMain() != base {
		t.Fatal("non-fast-forward candidate changed main")
	}
}
