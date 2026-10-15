package nodehost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type followerReader struct {
	snapshot func(context.Context, string) (store.RunSnapshot, error)
	node     func(context.Context, store.NodeRef) (store.NodeRow, error)
}

func (r followerReader) ReadSnapshot(ctx context.Context, run string) (store.RunSnapshot, error) {
	return r.snapshot(ctx, run)
}

func (r followerReader) ReadNode(ctx context.Context, ref store.NodeRef) (store.NodeRow, error) {
	return r.node(ctx, ref)
}

func TestStateFollowerCatchesUpAfterSubscribing(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "requeue", true: "terminal"}[terminal], func(t *testing.T) {
			ref := store.NodeRef{RunID: "run", NodeID: "build"}
			row := store.NodeRow{Ref: ref, StateSeq: 7, Status: store.StatusRunning, Generation: 2, Attempt: 5}
			subscribed, closed := false, false
			f := StateFollower{
				Store: followerReader{node: func(context.Context, store.NodeRef) (store.NodeRow, error) {
					if !subscribed {
						t.Fatal("read preceded subscription")
					}
					return row, nil
				}},
				Subscribe: func(_ context.Context, run string) (StateSubscription, error) {
					if run != ref.RunID {
						t.Fatalf("subscribed to %q", run)
					}
					subscribed = true
					row.StateSeq++
					row.Status = store.StatusPending
					if terminal {
						row.Status, row.Outcome = store.StatusDone, store.NodeSuccess
					}
					return StateSubscription{Changed: make(chan struct{}), Close: func() error { closed = true; return nil }}, nil
				},
			}
			if err := f.WaitNode(t.Context(), ref, 7); err != nil || !closed {
				t.Fatalf("wait = %v; closed = %v", err, closed)
			}
		})
	}
}

func TestStateFollowerNotificationsAreOnlyHints(t *testing.T) {
	ref := store.NodeRef{RunID: "run", NodeID: "build"}
	changed := make(chan struct{}, 1)
	changed <- struct{}{}
	reads := make(chan int)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	cause := errors.New("owner lost")
	f := StateFollower{
		Store: followerReader{node: func(ctx context.Context, _ store.NodeRef) (store.NodeRow, error) {
			select {
			case reads <- 1:
			case <-ctx.Done():
			}
			expired := time.Unix(0, 0)
			return store.NodeRow{Ref: ref, StateSeq: 7, Status: store.StatusRunning, LeaseExpiresAt: &expired}, nil
		}},
		Subscribe: func(context.Context, string) (StateSubscription, error) {
			return StateSubscription{Changed: changed, Close: func() error { return nil }}, nil
		},
	}
	go func() { done <- f.WaitNode(ctx, ref, 7) }()
	<-reads
	<-reads
	cancel(cause)
	if err := <-done; !errors.Is(err, cause) {
		t.Fatalf("hint was accepted as persisted progress: %v", err)
	}
}

func TestStateFollowerReadsCommittedChangeAfterNotification(t *testing.T) {
	ref := store.NodeRef{RunID: "run", NodeID: "build"}
	changed, read := make(chan struct{}, 1), make(chan struct{}, 1)
	var mu sync.Mutex
	sequence := int64(7)
	f := StateFollower{
		Store: followerReader{node: func(context.Context, store.NodeRef) (store.NodeRow, error) {
			mu.Lock()
			row := store.NodeRow{Ref: ref, StateSeq: sequence, Status: store.StatusRunning}
			mu.Unlock()
			select {
			case read <- struct{}{}:
			default:
			}
			return row, nil
		}},
		Subscribe: func(context.Context, string) (StateSubscription, error) {
			return StateSubscription{Changed: changed, Close: func() error { return nil }}, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- f.WaitNode(t.Context(), ref, 7) }()
	<-read
	mu.Lock()
	sequence++
	mu.Unlock()
	changed <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func followerSnapshot() store.RunSnapshot {
	return store.RunSnapshot{
		Run: store.RunRow{ID: "run", StateSeq: 3},
		Nodes: []store.NodeRow{
			{Ref: store.NodeRef{RunID: "run", NodeID: "build"}, StateSeq: 5},
			{Ref: store.NodeRef{RunID: "run", NodeID: "test"}, StateSeq: 1},
		},
	}
}

func TestStateFollowerWaitsForPersistedGraphChanges(t *testing.T) {
	for _, mode := range []string{"run", "job", "new job", "empty expansion", "failed expansion", "accepted empty plan", "reordered jobs", "store clock"} {
		t.Run(mode, func(t *testing.T) {
			before := followerSnapshot()
			current := followerSnapshot()
			switch mode {
			case "run":
				current.Run.StateSeq++
			case "job":
				current.Nodes[0].StateSeq++
			case "new job":
				current.Nodes = append(current.Nodes, store.NodeRow{Ref: store.NodeRef{RunID: "run", NodeID: "generated"}})
			case "empty expansion":
				current.Expansions = []store.AcceptedExpansionRow{{Source: current.Nodes[0].Ref, SourceGeneration: 0, BodyHash: "empty"}}
			case "failed expansion":
				before.Nodes[0].Expansion, current.Nodes[0].Expansion = store.ExpansionExpected, store.ExpansionFailed
			case "accepted empty plan":
				current.Run.Spec, current.Run.PlanHash = &runspec.Spec{Hash: "accepted"}, "empty-plan"
			case "reordered jobs":
				current.Nodes[0], current.Nodes[1] = current.Nodes[1], current.Nodes[0]
			case "store clock":
				current.StoreNow = time.Unix(60, 0)
			}
			f := StateFollower{
				Store: followerReader{snapshot: func(context.Context, string) (store.RunSnapshot, error) {
					return current, nil
				}},
				Subscribe: func(context.Context, string) (StateSubscription, error) {
					changed := make(chan struct{})
					close(changed)
					return StateSubscription{Changed: changed, Close: func() error { return nil }}, nil
				},
			}
			err := f.Wait(t.Context(), before)
			if mode == "reordered jobs" || mode == "store clock" {
				if err == nil || !strings.Contains(err.Error(), "subscription ended") {
					t.Fatalf("unchanged snapshot became progress: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStateFollowerJoinsSubscriptionOnEveryExit(t *testing.T) {
	for _, mode := range []string{"changed", "read failure", "subscribe failure", "closed stream", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			cause := errors.New("reader failed")
			closeErr := errors.New("subscription join failed")
			closed := 0
			f := StateFollower{
				Store: followerReader{snapshot: func(context.Context, string) (store.RunSnapshot, error) {
					if mode == "read failure" {
						return store.RunSnapshot{}, cause
					}
					current := followerSnapshot()
					if mode == "changed" {
						current.Run.StateSeq++
					}
					if mode == "cancelled" {
						cancel(cause)
					}
					return current, nil
				}},
				Subscribe: func(ctx context.Context, _ string) (StateSubscription, error) {
					changed := make(chan struct{})
					if mode == "closed stream" {
						close(changed)
					}
					var wg sync.WaitGroup
					wg.Add(1)
					go func() { defer wg.Done(); <-ctx.Done() }()
					s := StateSubscription{Changed: changed, Close: func() error { wg.Wait(); closed++; return closeErr }}
					if mode == "subscribe failure" {
						return s, cause
					}
					return s, nil
				},
			}
			err := f.Wait(ctx, followerSnapshot())
			if closed != 1 || !errors.Is(err, closeErr) || err == nil {
				t.Fatalf("wait = %v; closes = %d", err, closed)
			}
			if mode == "read failure" || mode == "subscribe failure" || mode == "cancelled" {
				if !errors.Is(err, cause) {
					t.Fatalf("cause lost: %v", err)
				}
			}
		})
	}
}

func TestStateFollowerRejectsRegressedOrForeignState(t *testing.T) {
	for _, mode := range []string{"run identity", "run sequence", "job identity", "duplicate job", "missing job", "job sequence", "expansion"} {
		t.Run(mode, func(t *testing.T) {
			before, current := followerSnapshot(), followerSnapshot()
			switch mode {
			case "run identity":
				current.Run.ID = "other"
			case "run sequence":
				current.Run.StateSeq--
			case "job identity":
				current.Nodes[0].Ref.RunID = "other"
			case "duplicate job":
				current.Nodes = append(current.Nodes, current.Nodes[0])
			case "missing job":
				current.Nodes = current.Nodes[1:]
			case "job sequence":
				current.Nodes[0].StateSeq--
			case "expansion":
				before.Expansions = []store.AcceptedExpansionRow{{Source: before.Nodes[0].Ref, BodyHash: "old"}}
				current.Expansions = []store.AcceptedExpansionRow{{Source: current.Nodes[0].Ref, BodyHash: "replaced"}}
			}
			f := StateFollower{
				Store: followerReader{snapshot: func(context.Context, string) (store.RunSnapshot, error) { return current, nil }},
				Subscribe: func(context.Context, string) (StateSubscription, error) {
					return StateSubscription{Changed: make(chan struct{}), Close: func() error { return nil }}, nil
				},
			}
			if err := f.Wait(t.Context(), before); err == nil {
				t.Fatal("invalid persistence view accepted")
			}
		})
	}
}
