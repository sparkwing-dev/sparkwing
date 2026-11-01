package launcher

import (
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestSharedPoolRBACDeniedPlaceholderDeleteReleasesReservation(t *testing.T) {
	cfg := apiServerAuthorizing(t, "RBAC")
	admin, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, fixture := boundaryFixture()
	setSharedClass(t, b, "4")
	node, err := fixture.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.ResourceVersion = ""
	node.UID = ""
	node, err = admin.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	if _, err = admin.CoreV1().Nodes().UpdateStatus(t.Context(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "warm"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.CoreV1().ServiceAccounts("warm").Create(t.Context(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = admin.SchedulingV1().PriorityClasses().Create(t.Context(), &schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: WarmPriorityClass}, Value: -1000}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	template := sharedWarmPod()
	template.Spec.AutomountServiceAccountToken = ptr(false)
	template.Spec.Containers = []corev1.Container{{Name: "pause", Image: "registry.k8s.io/pause:3.10"}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "warm", Namespace: "warm", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "sparkwing-cloud-warm-4", UID: "deployment-uid", Controller: ptr(true)}}}, Spec: appsv1.ReplicaSetSpec{Replicas: ptr(int32(0)), Selector: &metav1.LabelSelector{MatchLabels: template.Labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: template.Labels}, Spec: template.Spec}}}
	rs, err = admin.AppsV1().ReplicaSets("warm").Create(t.Context(), rs, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	template.UID = ""
	template.OwnerReferences[0].UID = rs.UID
	if _, err = admin.CoreV1().Pods("warm").Create(t.Context(), template, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "test-launcher"}, Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"get", "list", "update"}},
		{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list"}},
		{APIGroups: []string{"apps"}, Resources: []string{"replicasets"}, Verbs: []string{"get"}},
	}}
	if _, err = admin.RbacV1().ClusterRoles().Create(t.Context(), role, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "test-launcher"}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: role.Name}, Subjects: []rbacv1.Subject{{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "test-launcher"}}}
	if _, err = admin.RbacV1().ClusterRoleBindings().Create(t.Context(), binding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	restricted := rest.CopyConfig(cfg)
	restricted.Impersonate = rest.ImpersonationConfig{UserName: "test-launcher"}
	b.Kube, err = kubernetes.NewForConfig(restricted)
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Reserve(t.Context(), boundaryBinding("team"), "job", CloudPlacement{RequestedClass: "2", UpsizeEnabled: true})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("reserve: %v", err)
	}
	node, err = admin.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reservationEvidence(node) {
		t.Fatal("RBAC denial retained reservation")
	}
	if node.Labels[TeamNodeLabel] != TeamNodeValue("cloud", "team") || node.Labels[usedRunLabel] != runNodeValue("cloud", "run") {
		t.Fatalf("permanent binding changed: %v", node.Labels)
	}
	if _, err = admin.CoreV1().Pods("warm").Get(t.Context(), "warm", metav1.GetOptions{}); err != nil {
		t.Fatalf("placeholder: %v", err)
	}
	other := boundaryBinding("team")
	other.RunID = "another-run"
	if _, err = b.Reserve(t.Context(), other, "other", CloudPlacement{RequestedClass: "4"}); !errors.Is(err, ErrNoDedicatedNode) {
		t.Fatalf("foreign run: %v", err)
	}
}
