package launcher

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

const quarantineAnnotation = "sparkwing.dev/quarantined"

// errAwardMismatch marks a controller decision that disagrees with the award an observation carried.
var errAwardMismatch = errors.New("controller sync returned duplicate or changed Cloud bindings")

// nodeFault is a fault confined to one node, which never stops claims on
// other nodes. It has no Unwrap, so an API error cannot travel inside it and be
// discarded along with it.
type nodeFault struct{ node, cause string }

func (f *nodeFault) Error() string {
	return fmt.Sprintf("Cloud node %s: %s", f.node, f.cause)
}

// safety: a kubelet can rewrite its own Node's annotations, so a fault read
// from them is charged to that node alone. The node is cordoned and records
// the cause, and claiming continues for every other node.
func quarantineNode(ctx context.Context, kube kubernetes.Interface, node *corev1.Node, cause string) error {
	fault := &nodeFault{node: node.Name, cause: "quarantined: " + cause}
	if node.Labels[TeamNodeLabel] == "" || node.Spec.Unschedulable && node.Annotations[quarantineAnnotation] == cause {
		return fault
	}
	node = node.DeepCopy()
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	node.Annotations[quarantineAnnotation] = cause
	node.Spec.Unschedulable = true
	if _, err := kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
		fault.cause += "; cordon failed: " + err.Error()
	}
	return fault
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
