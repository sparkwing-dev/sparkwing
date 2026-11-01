package launcher

import (
	"context"
	"encoding/json"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const usedRunAnnotation = "sparkwing.dev/used-run"

// RunTerminal asks for an absorbing run state, independently of any job award.
func (c *ControllerControl) RunTerminal(ctx context.Context, team string, run runspec.RunID) (bool, error) {
	response, err := c.client.SyncLauncher(ctx, controllerwire.LauncherSyncRequest{RetireRun: &controllerwire.LauncherRun{Team: team, RunID: string(run)}})
	return response.RunTerminal, err
}

func (l *Launcher) syncSpentNodes(ctx context.Context) error {
	boundary, ok := l.Boundary.(*KubernetesBoundary)
	if !ok {
		return nil
	}
	control, ok := l.Control.(interface {
		RunTerminal(context.Context, string, runspec.RunID) (bool, error)
	})
	if !ok {
		return nil
	}
	nodes, err := l.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: PoolNamespaceLabel + "=" + l.Config.Namespace + "," + usedRunLabel})
	if err != nil {
		return err
	}
	var errs []error
	for _, node := range nodes.Items {
		if node.DeletionTimestamp != nil || reservationEvidence(&node) {
			continue
		}
		var run controllerwire.LauncherRun
		if json.Unmarshal([]byte(node.Annotations[usedRunAnnotation]), &run) != nil || run.Team == "" || run.RunID == "" || node.Labels[usedRunLabel] != runNodeValue(l.Config.Namespace, runspec.RunID(run.RunID)) || node.Labels[usedTeamLabel] != TeamNodeValue(l.Config.Namespace, run.Team) || node.Labels[TeamNodeLabel] != node.Labels[usedTeamLabel] {
			continue
		}
		terminal, err := control.RunTerminal(ctx, run.Team, runspec.RunID(run.RunID))
		if err == nil && terminal {
			err = boundary.retireSpentNode(ctx, node, run)
		}
		errs = append(errs, teardownFault(node.Name, err))
	}
	return errors.Join(errs...)
}

func (b *KubernetesBoundary) retireSpentNode(ctx context.Context, observed corev1.Node, run controllerwire.LauncherRun) error {
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
	node.Spec.Unschedulable = true
	if _, err = b.Kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
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
			return nil
		}
	}
	uid := claim.GetUID()
	err = b.Karpenter.Resource(nodeClaims).Delete(ctx, claim.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
