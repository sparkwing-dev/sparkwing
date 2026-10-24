package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

const quarantineAnnotation = "sparkwing.dev/quarantined"

// errAwardMismatch marks a controller decision that disagrees with the award an observation carried.
var errAwardMismatch = errors.New("controller sync returned duplicate or changed Cloud bindings")

// nodeFault names the suspect node for operator inspection. It has no Unwrap,
// so an API error cannot be discarded along with it.
type nodeFault struct{ node, cause string }

func (f *nodeFault) Error() string {
	return fmt.Sprintf("Cloud node %s: %s", f.node, f.cause)
}

// safety: a kubelet can rewrite its own annotations. Placement must reject
// damaged reservation bindings even when repeated conflicts prevent quarantine.
func quarantineNode(ctx context.Context, kube kubernetes.Interface, node *corev1.Node, namespace, cause string) error {
	fault := &nodeFault{node: node.Name, cause: cause}
	uid, team := node.UID, node.Labels[TeamNodeLabel]
	if uid == "" || team == "" || !ownsNodeReservation(node, namespace) {
		return fault
	}
	current := node
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if current == nil {
			fresh, err := kube.CoreV1().Nodes().Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			current = fresh
		}
		if current.UID != uid || current.Labels[TeamNodeLabel] != team || !ownsNodeReservation(current, namespace) {
			fault.cause = "ownership changed before quarantine"
			return fault
		}
		patch, err := quarantinePatch(current, cause)
		current = nil
		if err != nil || patch == nil {
			return err
		}
		_, err = kube.CoreV1().Nodes().Patch(ctx, node.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
		return err
	})
	switch {
	case err == nil:
		fault.cause = "quarantined: " + cause
		return fault
	case errors.Is(err, fault):
		return fault
	case apierrors.IsConflict(err):
		fault.cause += "; quarantine still conflicting, retrying next pass"
		return fault
	}
	return fmt.Errorf("quarantine Cloud node %s (%s): %w", node.Name, cause, err)
}

// Protected identity tests keep a concurrent replacement or team change outside the write.
func quarantinePatch(node *corev1.Node, cause string) ([]byte, error) {
	tainted := slices.ContainsFunc(node.Spec.Taints, func(t corev1.Taint) bool { return t.Key == quarantineAnnotation })
	if node.Spec.Unschedulable && tainted && node.Annotations[quarantineAnnotation] == cause {
		return nil, nil
	}
	ops := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": node.UID}, {"op": "test", "path": "/metadata/labels/" + strings.ReplaceAll(TeamNodeLabel, "/", "~1"), "value": node.Labels[TeamNodeLabel]}, {"op": "add", "path": "/spec/unschedulable", "value": true}}
	if node.Annotations == nil {
		ops = append(ops, map[string]any{"op": "add", "path": "/metadata/annotations", "value": map[string]string{quarantineAnnotation: cause}})
	} else {
		ops = append(ops, map[string]any{"op": "add", "path": "/metadata/annotations/" + strings.ReplaceAll(quarantineAnnotation, "/", "~1"), "value": cause})
	}
	taint := corev1.Taint{Key: quarantineAnnotation, Effect: corev1.TaintEffectNoSchedule}
	switch {
	case tainted:
	case node.Spec.Taints == nil:
		ops = append(ops, map[string]any{"op": "add", "path": "/spec/taints", "value": []corev1.Taint{taint}})
	default:
		ops = append(ops, map[string]any{"op": "add", "path": "/spec/taints/-", "value": taint})
	}
	return json.Marshal(ops)
}

func quarantined(node corev1.Node) bool {
	return node.Annotations[quarantineAnnotation] != "" || slices.ContainsFunc(node.Spec.Taints, func(t corev1.Taint) bool { return t.Key == quarantineAnnotation })
}

// blocking removes node faults from err, keeping the faults that must stop claims.
func blocking(err error) error {
	switch e := err.(type) { //nolint:errorlint // a wrapped node fault must stay blocking, so only the fault itself is dropped
	case *nodeFault:
		return nil
	case interface{ Unwrap() []error }:
		var kept []error
		for _, inner := range e.Unwrap() {
			kept = append(kept, blocking(inner))
		}
		return errors.Join(kept...)
	}
	return err
}

// reservationRefused reports a definitive controller verdict against one node's recorded award.
func reservationRefused(err error) bool {
	var failure *apierr.Error
	if errors.As(err, &failure) {
		return failure.Code == apierr.BadRequest || bindingRefused(err)
	}
	return errors.Is(err, errAwardMismatch)
}

func bindingRefused(err error) bool {
	var failure *apierr.Error
	return errors.As(err, &failure) && failure.Code == apierr.Forbidden &&
		(failure.Details["reason"] == apierr.ReasonBinding || failure.Details["reason"] == string(apierr.ReasonBinding))
}
