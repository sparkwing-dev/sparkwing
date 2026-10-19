package launcher

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// The fake clientset ignores RBAC, so this quarantines a node as the
// launcher's own chart-granted identity on a real, RBAC-authorizing API server.
func TestQuarantineUnderChartRBAC(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not installed; chart RBAC not exercised")
	}
	cfg := apiServerAuthorizing(t, "RBAC")
	chart := filepath.Join("..", "..", "..", "charts", "sparkwing-runner-bundle")
	digest := "@sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rendered, err := exec.Command(helm, "template", "sparkwing", chart, "--namespace", "cloud", "--show-only", "templates/launcher-rbac.yaml",
		"--set", "runner.enabled=false", "--set", "launcher.serviceAccountName=cloud-launcher",
		"--set", "launcher.jobImage.repository=registry.example/runner", "--set", "launcher.jobImage.tag=test", "--set", "launcher.jobImage.digest="+digest[1:],
		"--set", "launcher.dockerImage.repository=registry.example/docker", "--set", "launcher.dockerImage.tag=dind", "--set", "launcher.dockerImage.digest="+digest[1:]).CombinedOutput()
	if err != nil {
		t.Fatalf("render launcher RBAC: %v\n%s", err, rendered)
	}
	admin, ctx := impersonate(t, cfg, ""), t.Context()
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(rendered), 4096)
	installed := 0
	for {
		var object struct {
			Kind string `json:"kind"`
		}
		var raw rawDocument
		if err := decoder.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		switch object.Kind {
		case "ClusterRole":
			var role rbacv1.ClusterRole
			if err := json.Unmarshal(raw, &role); err != nil {
				t.Fatal(err)
			}
			_, err = admin.RbacV1().ClusterRoles().Create(ctx, &role, metav1.CreateOptions{})
		case "ClusterRoleBinding":
			var binding rbacv1.ClusterRoleBinding
			if err := json.Unmarshal(raw, &binding); err != nil {
				t.Fatal(err)
			}
			_, err = admin.RbacV1().ClusterRoleBindings().Create(ctx, &binding, metav1.CreateOptions{})
		default:
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		installed++
	}
	if installed != 2 {
		t.Fatalf("installed %d cluster RBAC objects, want the role and its binding", installed)
	}
	node, err := admin.CoreV1().Nodes().Create(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "tampered", Labels: map[string]string{TeamNodeLabel: TeamNodeValue("cloud", "team")}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	launcherClient := impersonate(t, cfg, "system:serviceaccount:cloud:cloud-launcher")
	if err := quarantineNode(ctx, launcherClient, node, "reservation is malformed or does not match its award"); blocking(err) != nil {
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

type rawDocument []byte

func (r *rawDocument) UnmarshalJSON(b []byte) error {
	*r = append((*r)[:0], b...)
	return nil
}
