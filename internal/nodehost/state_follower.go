package nodehost

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// stateReader supplies consistent persisted views for owners and holder followers.
type stateReader interface {
	ReadSnapshot(context.Context, string) (store.RunSnapshot, error)
	ReadNode(context.Context, controllerwire.NodeRef) (store.NodeRow, error)
}

// stateSubscription carries wake-ups for one run. Close must join the subscription
// after its context is cancelled. Notifications grant no authority and must cover
// every committed state change, including changes made by another process.
type stateSubscription struct {
	Changed <-chan struct{}
	Close   func() error
}

// stateFollower catches up from persistence after subscribing, closing the race
// between an owner's last snapshot and its wait. Subscribe must be armed before
// returning; its context bounds setup and remains live until Close.
// Assign Wait to OwnerHost.Wait and WaitNode to OwnerHost.WaitNode.
type stateFollower struct {
	Store     stateReader
	Subscribe func(context.Context, string) (stateSubscription, error)
}

// wait returns after persisted state or the owner's current execution eligibility changes.
func (f stateFollower) wait(ctx context.Context, before store.RunSnapshot) error {
	previous, err := snapshotVersion(before)
	if err != nil {
		return err
	}
	return f.follow(ctx, before.Run.ID, func(ctx context.Context) (bool, error) {
		snapshot, err := f.Store.ReadSnapshot(ctx, before.Run.ID)
		if err != nil {
			return false, err
		}
		current, err := snapshotVersion(snapshot)
		if err != nil {
			return false, err
		}
		return current.after(previous)
	})
}

// waitNode follows the node's state sequence rather than the run event cursor.
// A requeue wakes reconciliation but does not itself authorize execution.
func (f stateFollower) waitNode(ctx context.Context, ref controllerwire.NodeRef, after int64) error {
	if ref.RunID == "" || ref.NodeID == "" || after < 0 {
		return errors.New("state follower requires a node and nonnegative sequence")
	}
	return f.follow(ctx, string(ref.RunID), func(ctx context.Context) (bool, error) {
		row, err := f.Store.ReadNode(ctx, ref)
		if err != nil {
			return false, err
		}
		if row.Ref != ref || row.StateSeq < after {
			return false, errors.New("state follower received a foreign or regressed node")
		}
		return row.StateSeq > after, nil
	})
}

func (f stateFollower) follow(parent context.Context, run string, changed func(context.Context) (bool, error)) (err error) {
	if cause := context.Cause(parent); cause != nil {
		return cause
	}
	if f.Store == nil || f.Subscribe == nil {
		return errors.New("state follower requires persistence and subscription adapters")
	}
	ctx, cancel := context.WithCancel(parent)
	subscription, err := f.Subscribe(ctx, run)
	defer func() {
		cancel()
		if subscription.Close != nil {
			err = errors.Join(err, subscription.Close())
		}
		err = errors.Join(err, context.Cause(parent))
	}()
	if err != nil {
		return err
	}
	if subscription.Changed == nil || subscription.Close == nil {
		return errors.New("state follower received an incomplete subscription")
	}
	for {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		ready, err := changed(ctx)
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case _, ok := <-subscription.Changed:
			if !ok {
				return errors.New("state follower subscription ended before persisted progress")
			}
		}
	}
}

type expansionVersion struct {
	source     controllerwire.NodeRef
	generation int64
}

type stateVersion struct {
	run        string
	sequence   int64
	specHash   string
	planHash   string
	nodes      map[controllerwire.NodeRef]nodeVersion
	expansions map[expansionVersion]string
}

type nodeVersion struct {
	sequence   int64
	expansion  store.ExpansionState
	executable bool
}

func snapshotVersion(snapshot store.RunSnapshot) (stateVersion, error) {
	v := stateVersion{run: snapshot.Run.ID, sequence: snapshot.Run.StateSeq, planHash: snapshot.Run.PlanHash, nodes: map[controllerwire.NodeRef]nodeVersion{}, expansions: map[expansionVersion]string{}}
	if snapshot.Run.Spec != nil {
		v.specHash = string(snapshot.Run.Spec.Hash)
	}
	if v.run == "" || v.sequence < 0 {
		return v, errors.New("state follower received an invalid run sequence")
	}
	for _, row := range snapshot.Nodes {
		if _, duplicate := v.nodes[row.Ref]; duplicate || row.Ref.RunID != runspec.RunID(v.run) || row.Ref.NodeID == "" || row.StateSeq < 0 {
			return v, errors.New("state follower received an invalid job sequence")
		}
		v.nodes[row.Ref] = nodeVersion{row.StateSeq, row.Expansion, row.OwnerExecution != nil && row.OwnerExecution.Allowed}
	}
	for _, row := range snapshot.Expansions {
		key := expansionVersion{row.Source, row.SourceGeneration}
		if _, duplicate := v.expansions[key]; duplicate || row.Source.RunID != runspec.RunID(v.run) || row.Source.NodeID == "" || row.SourceGeneration < 0 || row.BodyHash == "" {
			return v, errors.New("state follower received an invalid expansion identity")
		}
		v.expansions[key] = row.BodyHash
	}
	return v, nil
}

func (v stateVersion) after(previous stateVersion) (bool, error) {
	if v.run != previous.run || v.sequence < previous.sequence {
		return false, errors.New("state follower received a foreign or regressed run")
	}
	if previous.specHash != "" && v.specHash != previous.specHash || previous.planHash != "" && v.planHash != previous.planHash {
		return false, errors.New("state follower received a changed run seal")
	}
	changed := v.sequence > previous.sequence || v.specHash != previous.specHash || v.planHash != previous.planHash || len(v.nodes) != len(previous.nodes) || len(v.expansions) != len(previous.expansions)
	for ref, node := range previous.nodes {
		current, exists := v.nodes[ref]
		if !exists || current.sequence < node.sequence {
			return false, errors.New("state follower lost persisted job progress")
		}
		changed = changed || current != node
	}
	for source, hash := range previous.expansions {
		if v.expansions[source] != hash {
			return false, errors.New("state follower lost an accepted expansion")
		}
	}
	return changed, nil
}

var _ stateReader = (*store.Tenant)(nil)
