package jobs

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type parallelReviewer func(context.Context, string, string, string) (reviewAnswer, error)

func (r parallelReviewer) review(ctx context.Context, dir, session, prompt string) (reviewAnswer, error) {
	return r(ctx, dir, session, prompt)
}

func TestReviewOverlapsProfilesAndJoinsBeforeSaving(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			fx := newReviewFixture(t)
			fx.git.files = []string{"cmd/x.go", "internal/store/billing_policy.go"}
			in := reviewInputs{branch: "feature/concurrent"}
			file := reviewRecordPath(fx.deps.recordDir, in.branch)
			prior := reviewRecord{Version: 1, Tree: "old", Findings: []reviewFinding{
				{ID: "F1", Reviewer: "default", Severity: "P1", Summary: "default defect", Status: findingOpen},
				{ID: "S1", Reviewer: "security", Severity: "P2", Summary: "security defect", Status: findingOpen},
			}}
			prior.setSession(reviewerRef{Profile: "default", Model: "fake-default", Session: "default-session"})
			prior.setSession(reviewerRef{Profile: "security", Model: "fake-security", Session: "security-session"})
			if err := saveReviewRecord(file, prior); err != nil {
				t.Fatal(err)
			}
			started := map[string]chan struct{}{"fake-default": make(chan struct{}), "fake-security": make(chan struct{})}
			var finished atomic.Int32
			refusal := errors.New("review unavailable")
			fx.deps.reviewer = func(model string) (reviewer, error) {
				return parallelReviewer(func(ctx context.Context, _, session, prompt string) (reviewAnswer, error) {
					defer finished.Add(1)
					other, wantSession, id := "fake-security", "default-session", "F1"
					if model == "fake-security" {
						other, wantSession, id = "fake-default", "security-session", "S1"
					}
					if fail {
						wantSession = ""
					}
					select {
					case <-started[model]:
					default:
						close(started[model])
					}
					select {
					case <-started[other]:
					case <-ctx.Done():
						return reviewAnswer{}, ctx.Err()
					}
					if session != wantSession {
						return reviewAnswer{}, fmt.Errorf("session %q, want %q", session, wantSession)
					}
					if fail && model == "fake-security" {
						return reviewAnswer{}, refusal
					}
					return reviewAnswer{session: session, text: fmt.Sprintf(`{"findings":[{"id":%q,"severity":"P1","summary":"fixed","status":"fixed"}]}`, id)}, nil
				}), nil
			}
			// safety: a serial implementation must fail instead of stranding the test at the rendezvous.
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			p := testPolicy(t)
			if fail {
				p.Rereview.SameReviewer = false
			}
			rec, _, err := runReview(ctx, fx.deps, p, in)
			if fail {
				if !errors.Is(err, refusal) || finished.Load() != 2 {
					t.Fatalf("error %v, want refusal", err)
				}
				saved, loadErr := loadReviewRecord(file)
				if loadErr != nil || saved.Tree != "old" {
					t.Fatalf("failed review replaced the record: %+v, %v", saved, loadErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if finished.Load() != 2 || rec.Result != resultPass || !rec.Passes[0].Resumed || !reflect.DeepEqual(rec.Passes[0].Profiles, []string{"default", "security"}) {
				t.Fatalf("finished %d, record %+v", finished.Load(), rec)
			}
			if len(rec.Findings) != 2 || rec.Findings[0].Summary != "default defect" || rec.Findings[1].Summary != "security defect" {
				t.Fatalf("merged findings %+v", rec.Findings)
			}
		})
	}
}
