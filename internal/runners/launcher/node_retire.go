package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const (
	usedRunAnnotation = "sparkwing.dev/used-run"
	// safety: marks a cordon the launcher made for retirement; an operator's cordon lacks it and is never undone.
	retirementCordonAnnotation = "sparkwing.dev/retirement-cordon"
)

type usedRun struct {
	Team  string `json:"team"`
	RunID string `json:"run_id"`
}

const (
	retirementInterval = 30 * time.Second
	// DefaultIdleGrace keeps a spent node for a successor that arrives after a short gap.
	DefaultIdleGrace = time.Minute
)

// safety: a ready successor must be claimed and reserved before an idle node can retire.
func (l *Launcher) idleGraceFloor() time.Duration {
	return l.claimPoll() + retirementInterval
}

func (l *Launcher) idleGrace() time.Duration {
	if l.IdleGrace == 0 {
		return max(DefaultIdleGrace, l.idleGraceFloor())
	}
	return l.IdleGrace
}

func (l *Launcher) validateIdleGrace() error {
	if l.IdleGrace != 0 && l.IdleGrace < l.idleGraceFloor() {
		return fmt.Errorf("idle grace %v must be at least the claim poll plus the %v retirement interval (%v)", l.IdleGrace, retirementInterval, l.idleGraceFloor())
	}
	return nil
}

func (l *Launcher) retireLoop(ctx context.Context) {
	// safety: spent-node passes run beside reconciliation so a slow one cannot delay award renewal or claims.
	ticker := time.NewTicker(retirementInterval)
	defer ticker.Stop()
	for {
		l.syncSpentNodes(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *Launcher) syncSpentNodes(ctx context.Context) {
	l.retirementMu.Lock()
	defer l.retirementMu.Unlock()
	if err := l.retireSpentNodes(ctx); err != nil && shutdownFault(ctx, err) != nil && (l.retirementLogged.IsZero() || time.Since(l.retirementLogged) >= time.Minute) {
		l.retirementLogged = time.Now()
		l.logger().Warn("spent node retirement failed; retrying on the next reconciliation", failureAttrs(err)...)
	}
}

func (l *Launcher) retireSpentNodes(ctx context.Context) error {
	boundary, ok := l.Boundary.(*KubernetesBoundary)
	if !ok {
		return nil
	}
	nodes, err := l.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: PoolNamespaceLabel + "=" + l.Config.Namespace + "," + usedRunLabel})
	if err != nil {
		return err
	}
	now := time.Now()
	idle := make(map[types.UID]time.Time)
	var errs []error
	for _, node := range nodes.Items {
		if node.DeletionTimestamp != nil || reservationEvidence(&node) {
			continue
		}
		var run usedRun
		if json.Unmarshal([]byte(node.Annotations[usedRunAnnotation]), &run) != nil || run.Team == "" || run.RunID == "" || node.Labels[usedRunLabel] != runNodeValue(l.Config.Namespace, runspec.RunID(run.RunID)) || node.Labels[usedTeamLabel] != TeamNodeValue(l.Config.Namespace, run.Team) || node.Labels[TeamNodeLabel] != node.Labels[usedTeamLabel] {
			continue
		}
		// safety: the run's state is never consulted, so an approval or a long sibling cannot hold an idle node.
		since, seen := l.idleSince[node.UID]
		if !seen {
			since = now
		}
		idle[node.UID] = since
		if now.Sub(since) >= l.idleGrace() {
			errs = append(errs, teardownFault(node.Name, boundary.retireSpentNode(ctx, node, run)))
		}
	}
	l.idleSince = idle
	return errors.Join(errs...)
}

func (b *KubernetesBoundary) retireSpentNode(ctx context.Context, observed corev1.Node, run usedRun) error {
	node, err := b.Kube.CoreV1().Nodes().Get(ctx, observed.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// safety: a concurrent reservation or replacement must win over this cleanup pass.
	if node.UID != observed.UID || node.ResourceVersion != observed.ResourceVersion || reservationEvidence(node) {
		return nil
	}
	if err = b.verifyNodeTeam(ctx, node, run.Team); errors.Is(err, ErrNoDedicatedNode) {
		return nil
	}
	if err != nil {
		return err
	}
	claim, err := b.nodeClaim(ctx, node)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// safety: cordon before the vacancy check so new placements cannot refill a spent node.
	if !node.Spec.Unschedulable {
		node.Annotations[retirementCordonAnnotation] = "true"
	}
	node.Spec.Unschedulable = true
	cordoned, err := b.Kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	pods, err := b.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == node.Name && !daemonPod(pod) {
			if cordoned.Annotations[retirementCordonAnnotation] == "" {
				return nil
			}
			// safety: a successor that bound before the cordon cannot reserve a cordoned node; a conflict retries next pass.
			delete(cordoned.Annotations, retirementCordonAnnotation)
			cordoned.Spec.Unschedulable = false
			_, err = b.Kube.CoreV1().Nodes().Update(ctx, cordoned, metav1.UpdateOptions{})
			if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
	}
	uid := claim.GetUID()
	err = b.Karpenter.Resource(nodeClaims).Delete(ctx, claim.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
