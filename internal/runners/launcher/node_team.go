package launcher

import (
	"context"
	"encoding/json"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const usedTeamLabel = "node-restriction.kubernetes.io/sparkwing-used-team"

var nodeClaims = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}
var nodePools = schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}

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

func (b *KubernetesBoundary) verifyNodeTeam(ctx context.Context, node *corev1.Node, team string) error {
	value := TeamNodeValue(team)
	if node.Labels[TeamNodeLabel] != value || (node.Labels[usedTeamLabel] != "" && node.Labels[usedTeamLabel] != value) {
		return ErrNoDedicatedNode
	}
	claim, err := b.nodeClaim(ctx, node)
	if apierrors.IsNotFound(err) {
		return ErrNoDedicatedNode
	}
	if err != nil {
		return err
	}
	if claim.GetDeletionTimestamp() != nil || claim.GetLabels()[TeamNodeLabel] != value {
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
		if pool.GetUID() == owner.UID && pool.GetDeletionTimestamp() == nil && claim.GetLabels()["karpenter.sh/nodepool"] == owner.Name && labels[TeamNodeLabel] == value {
			return nil
		}
	}
	return ErrNoDedicatedNode
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
		return errors.New("Cloud node was replaced before retirement")
	}
	var held CloudNodeReservation
	if err = json.Unmarshal([]byte(node.Annotations[reservationAnnotation]), &held); err != nil {
		return err
	}
	if held != r || node.Annotations[reservationNamespaceAnnotation] != b.Namespace {
		return errors.New("Cloud reservation changed before retirement")
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
