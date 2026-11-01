package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

const (
	CloudClassLabel      = "node-restriction.kubernetes.io/sparkwing-cloud-class"
	PoolNamespaceLabel   = "sparkwing.dev/launcher-namespace"
	WarmNamespaceLabel   = "sparkwing.dev/warm-namespace"
	DedicatedTaint       = "sparkwing.dev/dedicated"
	WarmPlaceholderLabel = "sparkwing.dev/warm-placeholder"
	WarmPriorityClass    = "sparkwing-cloud-warm"
	usedTeamLabel        = "node-restriction.kubernetes.io/sparkwing-used-team"
	// usedRunLabel records the one run a node may serve: a job of another run
	// must never find state an earlier run's job left behind, even within a team.
	usedRunLabel = "node-restriction.kubernetes.io/sparkwing-used-run"
)

func runNodeValue(namespace string, run runspec.RunID) string {
	sum := sha256.Sum256([]byte(namespace + "/" + string(run)))
	return "run-" + hex.EncodeToString(sum[:20])
}

var (
	nodeClaims = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}
	nodePools  = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
)

// CapacityConfigured checks pool templates, never current nodes, readiness or fleet limits.
func (b *KubernetesBoundary) CapacityConfigured(ctx context.Context, budget corev1.ResourceList) (bool, error) {
	if b.Karpenter == nil {
		return false, errors.New("cloud boundary requires Karpenter")
	}
	pools, err := b.Karpenter.Resource(nodePools).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, err
	}
	value := cpuClass(budget.Cpu().AsApproximateFloat64())
	for _, pool := range pools.Items {
		compatible, err := compatibleSharedPool(pool, b.Namespace, value, budget)
		if err != nil || compatible {
			return compatible, err
		}
	}
	return false, nil
}

func compatibleSharedPool(pool unstructured.Unstructured, namespace, class string, budget corev1.ResourceList) (bool, error) {
	labels, _, err := unstructured.NestedStringMap(pool.Object, "spec", "template", "metadata", "labels")
	if err != nil || class == "" || labels[PoolNamespaceLabel] != namespace || labels[CloudClassLabel] != class || labels[TeamNodeLabel] != "" {
		return false, err
	}
	taints, _, err := unstructured.NestedSlice(pool.Object, "spec", "template", "spec", "taints")
	if err != nil {
		return false, err
	}
	protected := false
	for _, raw := range taints {
		taint, ok := raw.(map[string]any)
		if !ok {
			return false, errors.New("invalid NodePool taint")
		}
		if taint["key"] == DedicatedTaint && taint["value"] == "true" && taint["effect"] == string(corev1.TaintEffectNoSchedule) {
			protected = true
		}
	}
	if !protected {
		return false, nil
	}
	// safety: only contradictory template constraints prove incompatibility; readiness and fleet limits can recover.
	known := map[string]string{CloudClassLabel: class, PoolNamespaceLabel: namespace, corev1.LabelOSStable: "linux"}
	if os, ok := labels[corev1.LabelOSStable]; ok && os != "linux" {
		return false, nil
	}
	requirements, _, err := unstructured.NestedSlice(pool.Object, "spec", "template", "spec", "requirements")
	if err != nil {
		return false, err
	}
	for _, raw := range requirements {
		requirement, ok := raw.(map[string]any)
		if !ok {
			return false, errors.New("invalid NodePool requirement")
		}
		key, _ := requirement["key"].(string)
		minimum := float64(0)
		switch key {
		case "karpenter.k8s.aws/instance-cpu":
			minimum = budget.Cpu().AsApproximateFloat64()
		case "karpenter.k8s.aws/instance-memory":
			minimum = float64(budget.Memory().Value()) / (1 << 20)
		}
		if minimum > 0 {
			allows, err := poolRequirementAllowsSize(requirement, minimum)
			if err != nil || !allows {
				return false, err
			}
		}
		wanted, fixed := known[key]
		if !fixed {
			continue
		}
		values, _, err := unstructured.NestedStringSlice(requirement, "values")
		if err != nil {
			return false, err
		}
		switch requirement["operator"] {
		case "In":
			if !slices.Contains(values, wanted) {
				return false, nil
			}
		case "NotIn":
			if slices.Contains(values, wanted) {
				return false, nil
			}
		case "DoesNotExist":
			return false, nil
		case "Exists":
		case "Gt", "Lt":
			return false, nil
		default:
			return false, fmt.Errorf("unknown NodePool requirement operator %v", requirement["operator"])
		}
	}
	return true, nil
}

// safety: only explicit upper bounds prove the awarded hardware class cannot fit.
func poolRequirementAllowsSize(requirement map[string]any, minimum float64) (bool, error) {
	values, _, err := unstructured.NestedStringSlice(requirement, "values")
	if err != nil {
		return false, err
	}
	switch requirement["operator"] {
	case "In", "Lt":
		for _, value := range values {
			number, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return false, err
			}
			if requirement["operator"] == "In" && number >= minimum || requirement["operator"] == "Lt" && number > minimum {
				return true, nil
			}
		}
		return false, nil
	case "DoesNotExist":
		return false, nil
	default:
		return true, nil
	}
}

func (b *KubernetesBoundary) nodeClaim(ctx context.Context, node *corev1.Node) (*unstructured.Unstructured, error) {
	if b.Karpenter == nil {
		return nil, errors.New("Cloud boundary requires Karpenter")
	}
	for _, owner := range node.OwnerReferences {
		if owner.APIVersion != "karpenter.sh/v1" || owner.Kind != "NodeClaim" || owner.Name == "" || owner.UID == "" {
			continue
		}
		claim, err := b.Karpenter.Resource(nodeClaims).Get(ctx, owner.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		name, _, _ := unstructured.NestedString(claim.Object, "status", "nodeName")
		provider, _, _ := unstructured.NestedString(claim.Object, "status", "providerID")
		if claim.GetUID() != owner.UID || name != node.Name || provider == "" || provider != node.Spec.ProviderID {
			return nil, ErrNoDedicatedNode
		}
		return claim, nil
	}
	return nil, ErrNoDedicatedNode
}

func cpuClass(cores float64) string {
	for _, size := range match.CloudSizes() {
		if size.Resources.Cores == cores {
			return strconv.Itoa(int(cores))
		}
	}
	return ""
}

func nextCPUClass(class string) string {
	sizes := match.CloudSizes()
	for i, size := range sizes {
		if cpuClass(size.Resources.Cores) == class && i+1 < len(sizes) {
			return cpuClass(sizes[i+1].Resources.Cores)
		}
	}
	return ""
}

func (b *KubernetesBoundary) verifyNodeTeam(ctx context.Context, node *corev1.Node, team string) error {
	value := TeamNodeValue(b.Namespace, team)
	usedTeam, usedRun := node.Labels[usedTeamLabel], node.Labels[usedRunLabel]
	// safety: an incomplete or foreign binding must never become fresh again; all three labels bind atomically.
	if usedTeam != "" || usedRun != "" || node.Labels[TeamNodeLabel] != "" {
		if usedTeam != value || usedRun == "" || node.Labels[TeamNodeLabel] != value {
			return ErrNoDedicatedNode
		}
	}
	class := node.Labels[CloudClassLabel]
	if class == "" || node.Labels[PoolNamespaceLabel] != b.Namespace || !hasDedicatedTaint(node.Spec.Taints) {
		return ErrNoDedicatedNode
	}
	claim, err := b.nodeClaim(ctx, node)
	if apierrors.IsNotFound(err) {
		return ErrNoDedicatedNode
	}
	if err != nil {
		return err
	}
	if claim.GetDeletionTimestamp() != nil || claim.GetLabels()[TeamNodeLabel] != "" {
		return ErrNoDedicatedNode
	}
	for _, owner := range claim.GetOwnerReferences() {
		if owner.APIVersion != "karpenter.sh/v1" || owner.Kind != "NodePool" || owner.Name == "" || owner.UID == "" {
			continue
		}
		pool, err := b.Karpenter.Resource(nodePools).Get(ctx, owner.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return ErrNoDedicatedNode
		}
		if err != nil {
			return err
		}
		labels, _, err := unstructured.NestedStringMap(pool.Object, "spec", "template", "metadata", "labels")
		if err != nil {
			return err
		}
		compatible, err := compatibleSharedPool(*pool, b.Namespace, class, corev1.ResourceList{})
		if err != nil {
			return err
		}
		if pool.GetUID() != owner.UID || pool.GetDeletionTimestamp() != nil || claim.GetLabels()["karpenter.sh/nodepool"] != owner.Name || !compatible {
			return ErrNoDedicatedNode
		}
		for _, key := range []string{CloudClassLabel, PoolNamespaceLabel, WarmNamespaceLabel} {
			if node.Labels[key] != labels[key] || claim.GetLabels()[key] != labels[key] {
				return ErrNoDedicatedNode
			}
		}
		return nil
	}
	return ErrNoDedicatedNode
}

func hasDedicatedTaint(taints []corev1.Taint) bool {
	return slices.ContainsFunc(taints, func(t corev1.Taint) bool {
		return t.Key == DedicatedTaint && t.Value == "true" && t.Effect == corev1.TaintEffectNoSchedule
	})
}

func warmPlaceholder(pod corev1.Pod, namespace string) bool {
	if namespace == "" || pod.Namespace != namespace || pod.Labels[WarmPlaceholderLabel] != "true" || pod.Spec.PriorityClassName != WarmPriorityClass || pod.Spec.Priority == nil || *pod.Spec.Priority >= 0 {
		return false
	}
	for _, owner := range pod.OwnerReferences {
		if owner.APIVersion == "apps/v1" && owner.Kind == "ReplicaSet" && owner.UID != "" && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func (b *KubernetesBoundary) hasWarmPlaceholder(ctx context.Context, node corev1.Node) (bool, error) {
	namespace := node.Labels[WarmNamespaceLabel]
	if namespace == "" {
		return false, nil
	}
	pods, err := b.Kube.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name})
	if err != nil {
		return false, err
	}
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == node.Name && warmPlaceholder(pod, node.Labels[WarmNamespaceLabel]) {
			return true, nil
		}
	}
	return false, nil
}

// Retire cordons the exact reserved node and deletes its owning NodeClaim by UID.
func (b *KubernetesBoundary) Retire(ctx context.Context, r CloudNodeReservation) error {
	node, err := b.Kube.CoreV1().Nodes().Get(ctx, r.NodeName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if string(node.UID) != r.NodeUID {
		return &nodeFault{node: r.NodeName, cause: "replaced before retirement"}
	}
	// safety: the node may now hold another award, so it is left untouched.
	var held CloudNodeReservation
	if json.Unmarshal([]byte(node.Annotations[reservationAnnotation]), &held) != nil || held != r || node.Annotations[reservationNamespaceAnnotation] != b.Namespace {
		return &nodeFault{node: r.NodeName, cause: "reservation changed before retirement"}
	}
	claim, claimErr := b.nodeClaim(ctx, node)
	node.Spec.Unschedulable = true
	if _, err = b.Kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		return err
	}
	if apierrors.IsNotFound(claimErr) {
		return nil
	}
	if claimErr != nil {
		return claimErr
	}
	uid := claim.GetUID()
	err = b.Karpenter.Resource(nodeClaims).Delete(ctx, claim.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
