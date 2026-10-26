package launcher

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

// ErrNoDedicatedNode leaves the award unstarted when no exclusive placement is available.
var ErrNoDedicatedNode = errors.New("no unoccupied team-dedicated Cloud node")

const (
	reservationNamespaceAnnotation = "sparkwing.dev/reservation-namespace"
	reservationTaint               = "sparkwing.dev/cloud-reserved"
	capacityRemoval                = 30 * time.Second
)

// KubernetesBoundary persists exclusivity on the node under Kubernetes resource-version fencing.
// Probe proves network enforcement and Clear removes prior Job state before the node can be reused.
// Both callbacks are required deployment integrations, with no manifest-only or no-op fallback.
// Clear must be idempotent and confined to this reservation's state, including concurrent calls.
type KubernetesBoundary struct {
	Kube       kubernetes.Interface
	Karpenter  dynamic.Interface
	Namespace  string
	Probe      func(context.Context, CloudNodeReservation) (CloudNetworkEvidence, error)
	Clear      func(context.Context, CloudNodeReservation) error
	StopProbes func(context.Context, CloudNodeReservation) error
}

func (b *KubernetesBoundary) Reserve(ctx context.Context, binding CloudJobBinding, job string) (CloudNodeReservation, CloudNetworkEvidence, error) {
	if b.Kube == nil || b.Namespace == "" || b.Probe == nil || b.Clear == nil || binding.Team == "" || binding.RunID == "" || job == "" {
		return CloudNodeReservation{}, CloudNetworkEvidence{}, errors.New("Cloud boundary requires Kubernetes, namespace, network probe, node cleanup, team, run and Job")
	}
	if _, err := cloudDeadline(time.Now(), binding); err != nil {
		return CloudNodeReservation{}, CloudNetworkEvidence{}, err
	}
	lease := binding.LeaseRef()
	if lease.Kind == "" || lease.ID == "" || lease.Holder == "" || lease.Generation <= 0 {
		return CloudNodeReservation{}, CloudNetworkEvidence{}, errors.New("Cloud reservation requires a complete award fence")
	}
	nodes, err := b.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: TeamNodeLabel + "=" + TeamNodeValue(b.Namespace, binding.Team)})
	if err != nil {
		return CloudNodeReservation{}, CloudNetworkEvidence{}, err
	}
	for _, node := range nodes.Items {
		if !nodeReady(node) || quarantined(node) {
			continue
		}
		reservation, err := b.reserveNode(ctx, node, binding, job)
		if apierrors.IsConflict(err) || errors.Is(err, ErrNoDedicatedNode) {
			continue
		}
		if err != nil {
			return CloudNodeReservation{}, CloudNetworkEvidence{}, err
		}
		err = b.consumeCapacity(ctx, node.Name, binding.Team)
		var evidence CloudNetworkEvidence
		if err == nil {
			evidence, err = b.Probe(ctx, reservation)
		}
		if err == nil {
			err = validateBoundary(b.Namespace, binding, job, reservation, evidence)
		}
		if err != nil {
			return CloudNodeReservation{}, CloudNetworkEvidence{}, errors.Join(err, b.Release(ctx, reservation))
		}
		return reservation, evidence, nil
	}
	return CloudNodeReservation{}, CloudNetworkEvidence{}, ErrNoDedicatedNode
}

func (b *KubernetesBoundary) reserveNode(ctx context.Context, node corev1.Node, binding CloudJobBinding, job string) (CloudNodeReservation, error) {
	team := binding.Team
	if err := b.verifyNodeTeam(ctx, &node, team); err != nil {
		return CloudNodeReservation{}, err
	}
	run := runNodeValue(b.Namespace, binding.RunID)
	if (node.Labels[usedTeamLabel] != "" || node.Labels[usedRunLabel] != "") && node.Labels[usedRunLabel] != run {
		return CloudNodeReservation{}, ErrNoDedicatedNode
	}
	// safety: replay must validate the binding too, even when quarantine could not persist.
	if reservationEvidence(&node) {
		held, recorded, ok := nodeReservation(&node)
		if ok && !quarantined(node) && held.JobName == job && node.Annotations[reservationNamespaceAnnotation] == b.Namespace && sameBinding(recorded, binding) {
			return held, nil
		}
		return CloudNodeReservation{}, ErrNoDedicatedNode
	}
	if _, err := b.occupants(ctx, node.Name, team); err != nil {
		return CloudNodeReservation{}, err
	}
	r := CloudNodeReservation{NodeName: node.Name, NodeUID: string(node.UID), Team: team, JobName: job, ReservationID: rand.Text(), Award: binding.LeaseRef()}
	if node.UID == "" {
		return CloudNodeReservation{}, errors.New("Cloud node has no Kubernetes UID")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return CloudNodeReservation{}, err
	}
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		return CloudNodeReservation{}, err
	}
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	node.Labels[usedTeamLabel] = TeamNodeValue(b.Namespace, team)
	node.Labels[usedRunLabel] = run
	node.Annotations[reservationAnnotation] = string(raw)
	node.Annotations[reservationNamespaceAnnotation] = b.Namespace
	node.Annotations[bindingAnnotation] = string(bindingJSON)
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{Key: reservationTaint, Value: r.ReservationID, Effect: corev1.TaintEffectNoSchedule})
	if _, err = b.Kube.CoreV1().Nodes().Update(ctx, &node, metav1.UpdateOptions{}); err != nil {
		return CloudNodeReservation{}, err
	}
	return r, nil
}

// Release frees the node. A kubelet rewriting its own annotations can make
// every update conflict; that is charged to the node, which stays reserved and
// therefore out of placement, and sync retries it.
func (b *KubernetesBoundary) Release(ctx context.Context, reservation CloudNodeReservation) error {
	if b.Kube == nil || b.Clear == nil {
		return errors.New("Cloud boundary requires Kubernetes and node cleanup")
	}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error { return b.release(ctx, reservation) })
	if apierrors.IsConflict(err) {
		return &nodeFault{node: reservation.NodeName, cause: "release still conflicting, retrying next pass"}
	}
	return err
}

func (b *KubernetesBoundary) release(ctx context.Context, reservation CloudNodeReservation) error {
	node, err := b.Kube.CoreV1().Nodes().Get(ctx, reservation.NodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(node.UID) != reservation.NodeUID {
		return &nodeFault{node: reservation.NodeName, cause: "replaced before teardown"}
	}
	raw := node.Annotations[reservationAnnotation]
	if raw == "" {
		return nil
	}
	// safety: the node may now hold another award, so it is left untouched.
	var held CloudNodeReservation
	if json.Unmarshal([]byte(raw), &held) != nil || held != reservation || node.Annotations[reservationNamespaceAnnotation] != b.Namespace {
		return &nodeFault{node: reservation.NodeName, cause: "reservation changed before teardown"}
	}
	if b.StopProbes != nil {
		if err = b.StopProbes(ctx, reservation); err != nil {
			return err
		}
	}
	if err = b.vacant(ctx, node.Name); err != nil {
		if errors.Is(err, ErrNoDedicatedNode) {
			return errContainersRunning
		}
		return err
	}
	if err = b.Clear(ctx, reservation); err != nil {
		return fmt.Errorf("clear Cloud node state: %w", err)
	}
	delete(node.Annotations, reservationAnnotation)
	delete(node.Annotations, reservationNamespaceAnnotation)
	delete(node.Annotations, bindingAnnotation)
	node.Spec.Taints = slices.DeleteFunc(node.Spec.Taints, func(taint corev1.Taint) bool {
		return taint.Key == reservationTaint && taint.Value == reservation.ReservationID
	})
	_, err = b.Kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	return err
}

func (b *KubernetesBoundary) vacant(ctx context.Context, node string) error {
	_, err := b.occupants(ctx, node, "")
	return err
}

// safety: only the team's own unowned requests in the launcher namespace are
// consumable; any other non-DaemonSet Pod keeps the node occupied.
func (b *KubernetesBoundary) occupants(ctx context.Context, node, team string) ([]corev1.Pod, error) {
	pods, err := b.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
	if err != nil {
		return nil, err
	}
	var requests []corev1.Pod
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != node {
			continue
		}
		if daemonPod(pod) {
			continue
		}
		if team != "" && pod.Namespace == b.Namespace && strings.HasPrefix(pod.Name, capacityPrefix) && len(pod.OwnerReferences) == 0 && pod.Labels[capacityLabel] == "true" && pod.Labels[TeamLabel] == TeamNodeValue(b.Namespace, team) {
			requests = append(requests, pod)
			continue
		}
		return nil, ErrNoDedicatedNode
	}
	return requests, nil
}

// safety: requests leave only after the reservation and its taint are persisted,
// so no other award can take the node. consolidateAfter must outlast empty gaps
// before and after boundary probes. Waiting for removal frees the requests'
// resources before probing and Job placement.
func (b *KubernetesBoundary) consumeCapacity(ctx context.Context, node, team string) error {
	requests, err := b.occupants(ctx, node, team)
	if err != nil {
		return err
	}
	pods := b.Kube.CoreV1().Pods(b.Namespace)
	for _, pod := range requests {
		err := pods.Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &pod.UID}})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		err = wait.PollUntilContextTimeout(ctx, 250*time.Millisecond, capacityRemoval, true, func(ctx context.Context) (bool, error) {
			current, err := pods.Get(ctx, pod.Name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return err == nil && current.UID != pod.UID, err
		})
		if err != nil {
			return fmt.Errorf("capacity request %s did not leave node %s: %w", pod.Name, node, err)
		}
	}
	return nil
}

func nodeReady(node corev1.Node) bool {
	if node.Spec.Unschedulable || node.DeletionTimestamp != nil {
		return false
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// TeamNodeValue is the collision-resistant value infrastructure must place on the team's dedicated nodes.
func TeamNodeValue(namespace, team string) string { return admission.TeamNodeValue(namespace, team) }

func daemonPod(pod corev1.Pod) bool {
	for _, owner := range pod.OwnerReferences {
		if owner.APIVersion == "apps/v1" && owner.Kind == "DaemonSet" && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}
