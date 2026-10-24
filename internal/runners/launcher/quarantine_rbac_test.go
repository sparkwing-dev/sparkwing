package launcher

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The fake clientset ignores RBAC, so this quarantines a node as the
// launcher's own chart-granted identity on a real, RBAC-authorizing API server.
func TestQuarantineUnderChartRBAC(t *testing.T) {
	cfg := apiServerAuthorizing(t, "RBAC")
	installConfinementChart(t, cfg, "sparkwing-runner-bundle")
	admin, ctx := impersonate(t, cfg, ""), t.Context()
	node, err := admin.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "tampered", Labels: map[string]string{TeamNodeLabel: TeamNodeValue("cloud", "team")}, Annotations: reservedNode(t, "tampered", boundaryBinding("team"), "garbage").Annotations}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	launcherClient := impersonate(t, cfg, "system:serviceaccount:cloud:cloud-launcher")
	if err := quarantineNode(ctx, launcherClient, node, "cloud", "reservation is malformed or does not match its award"); blocking(err) != nil {
		t.Fatalf("the chart's launcher identity cannot quarantine a node: %v", err)
	}
	got, err := admin.CoreV1().Nodes().Get(ctx, "tampered", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Spec.Unschedulable || !slices.ContainsFunc(got.Spec.Taints, func(t corev1.Taint) bool { return t.Key == quarantineAnnotation }) || got.Annotations[quarantineAnnotation] == "" {
		t.Fatalf("quarantine did not persist: %+v %v", got.Spec, got.Annotations)
	}
}
