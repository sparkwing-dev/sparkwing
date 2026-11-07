package launcher

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Two NodePools of different EC2NodeClasses can carry the same class, namespace
// and taint, as staging's boot and jobs pools did. A capacity request must select
// exactly the nodes the boundary accepts for the launcher's node class.
func TestCapacityRequestSelectsOnlyNodesTheBoundaryAccepts(t *testing.T) {
	for _, launcherClass := range []string{"sparkwing-jobs", "sparkwing-staging-boot"} {
		t.Run(launcherClass, func(t *testing.T) {
			b, kube := boundaryFixture()
			b.NodeClassName = launcherClass
			node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			award := teamAward("team")
			pod, err := capacityPod(CloudJobConfig{Namespace: "cloud", NodeClassName: launcherClass, RunnerImage: "runner", CPUCeiling: 4, MemoryCeiling: 8 << 30}, demandFor(award))
			if err != nil {
				t.Fatal(err)
			}
			selected := (&pool{t: t}).fits(*pod, *node, nil)
			accepted := b.verifyNodeTeam(t.Context(), node.DeepCopy(), award.Binding.Team) == nil
			if selected != accepted || accepted != (launcherClass == node.Labels[NodeClassLabel]) {
				t.Fatalf("node of class %s: capacity request selects=%t, boundary accepts=%t", node.Labels[NodeClassLabel], selected, accepted)
			}
		})
	}
}

// A request bound to a node the boundary refuses is reported on every pass
// instead of waiting silently for its award deadline.
func TestCapacityRequestOnRefusedNodeWarnsEveryPass(t *testing.T) {
	for _, launcherClass := range []string{"sparkwing-jobs", "sparkwing-staging-boot"} {
		t.Run(launcherClass, func(t *testing.T) {
			l, kube, _, control := fixture()
			l.Config.NodeClassName = launcherClass
			l.Boundary = &KubernetesBoundary{Kube: kube, Karpenter: karpenterFixture(), Namespace: "cloud", NodeClassName: launcherClass}
			var logs bytes.Buffer
			l.Logger = slog.New(slog.NewTextHandler(&logs, nil))
			_, source := boundaryFixture()
			node, err := source.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := kube.CoreV1().Nodes().Create(t.Context(), node, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := l.requestCapacity(t.Context(), *control.award); err != nil {
				t.Fatal(err)
			}
			pod := capacityPods(t, kube)[0]
			pod.UID, pod.Spec.NodeName = "bound", node.Name
			if _, err := kube.CoreV1().Pods("cloud").Update(t.Context(), &pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			refused := launcherClass != node.Labels[NodeClassLabel]
			for range 2 {
				if err := l.requestCapacity(t.Context(), *control.award); err != nil {
					t.Fatal(err)
				}
				if left := capacityPods(t, kube); len(left) != 1 || left[0].UID != pod.UID {
					t.Fatalf("bound request churned: %+v", left)
				}
				warning := logs.String()
				if !refused {
					if warning != "" {
						t.Fatalf("accepted node logged: %s", warning)
					}
					continue
				}
				for _, want := range []string{"level=WARN", "node=node", "EC2NodeClass " + launcherClass, "node_class=" + launcherClass} {
					if !strings.Contains(warning, want) {
						t.Fatalf("refusal warning %q omits %q", warning, want)
					}
				}
				logs.Reset()
			}
		})
	}
}
