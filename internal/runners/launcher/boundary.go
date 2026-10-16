package launcher

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ErrNoDedicatedNode leaves the award unstarted when no exclusive placement is available.
var ErrNoDedicatedNode = errors.New("no unoccupied team-dedicated Cloud node")

const (
	reservationNamespaceAnnotation = "sparkwing.dev/reservation-namespace"
	reservationTaint               = "sparkwing.dev/cloud-reserved"
)

// KubernetesBoundary persists exclusivity on the node under Kubernetes resource-version fencing.
// Probe proves network enforcement and Clear removes prior Job state before the node can be reused.
// Both callbacks are required deployment integrations, with no manifest-only or no-op fallback.
// Clear must be idempotent and confined to this reservation's state, including concurrent calls.
type KubernetesBoundary struct {
	Kube       kubernetes.Interface
	Namespace  string
	Probe      func(context.Context, CloudNodeReservation) (CloudNetworkEvidence, error)
	Clear      func(context.Context, CloudNodeReservation) error
	StopProbes func(context.Context, CloudNodeReservation) error
}

func (b *KubernetesBoundary) Reserve(ctx context.Context, binding CloudJobBinding, job string) (CloudNodeReservation, CloudNetworkEvidence, error) {
	if b.Kube == nil || b.Namespace == "" || b.Probe == nil || b.Clear == nil || binding.Team == "" || job == "" {
		return CloudNodeReservation{}, CloudNetworkEvidence{}, errors.New("Cloud boundary requires Kubernetes, namespace, network probe, node cleanup, team and Job")
	}
	if _, err := cloudDeadline(time.Now(), binding); err != nil {
		return CloudNodeReservation{}, CloudNetworkEvidence{}, err
	}
	lease := binding.LeaseRef()
	if lease.Kind == "" || lease.ID == "" || lease.Holder == "" || lease.Generation <= 0 {
		return CloudNodeReservation{}, CloudNetworkEvidence{}, errors.New("Cloud reservation requires a complete award fence")
	}
	nodes, err := b.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: TeamNodeLabel + "=" + TeamNodeValue(binding.Team)})
	if err != nil {
		return CloudNodeReservation{}, CloudNetworkEvidence{}, err
	}
	for _, node := range nodes.Items {
		if !nodeReady(node) {
			continue
		}
		reservation, err := b.reserveNode(ctx, node, binding, job)
		if apierrors.IsConflict(err) || errors.Is(err, ErrNoDedicatedNode) {
			continue
		}
		if err != nil {
			return CloudNodeReservation{}, CloudNetworkEvidence{}, err
		}
		evidence, err := b.Probe(ctx, reservation)
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
	if raw := node.Annotations[reservationAnnotation]; raw != "" {
		var held CloudNodeReservation
		if err := json.Unmarshal([]byte(raw), &held); err != nil {
			return held, fmt.Errorf("node %s has a malformed Cloud reservation", node.Name)
		}
		if held.Team == team && held.JobName == job && held.NodeUID == string(node.UID) && held.NodeName == node.Name && held.ReservationID != "" && held.Award == binding.LeaseRef() && node.Annotations[reservationNamespaceAnnotation] == b.Namespace {
			return held, nil
		}
		return CloudNodeReservation{}, ErrNoDedicatedNode
	}
	if err := b.vacant(ctx, node.Name); err != nil {
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
	node.Annotations[reservationAnnotation] = string(raw)
	node.Annotations[reservationNamespaceAnnotation] = b.Namespace
	node.Annotations[bindingAnnotation] = string(bindingJSON)
	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{Key: reservationTaint, Value: r.ReservationID, Effect: corev1.TaintEffectNoSchedule})
	if _, err = b.Kube.CoreV1().Nodes().Update(ctx, &node, metav1.UpdateOptions{}); err != nil {
		return CloudNodeReservation{}, err
	}
	return r, nil
}

func (b *KubernetesBoundary) Release(ctx context.Context, reservation CloudNodeReservation) error {
	if b.Kube == nil || b.Clear == nil {
		return errors.New("Cloud boundary requires Kubernetes and node cleanup")
	}
	node, err := b.Kube.CoreV1().Nodes().Get(ctx, reservation.NodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(node.UID) != reservation.NodeUID {
		return errors.New("Cloud node was replaced before teardown")
	}
	raw := node.Annotations[reservationAnnotation]
	if raw == "" {
		return nil
	}
	var held CloudNodeReservation
	if err = json.Unmarshal([]byte(raw), &held); err != nil {
		return err
	}
	if held != reservation || node.Annotations[reservationNamespaceAnnotation] != b.Namespace {
		return errors.New("Cloud reservation changed before teardown")
	}
	if b.StopProbes != nil {
		if err = b.StopProbes(ctx, reservation); err != nil {
			return err
		}
	}
	if err = b.vacant(ctx, node.Name); err != nil {
		if errors.Is(err, ErrNoDedicatedNode) {
			return ErrContainersRunning
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
	pods, err := b.Kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != node {
			continue
		}
		daemon := false
		for _, owner := range pod.OwnerReferences {
			if owner.Kind == "DaemonSet" && owner.Controller != nil && *owner.Controller {
				daemon = true
			}
		}
		if !daemon {
			return ErrNoDedicatedNode
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
func TeamNodeValue(team string) string { return labelHash(team) }
