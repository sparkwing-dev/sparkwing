package launcher

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Karpenter refuses to provision for a Pod whose affinity names one of its
// restricted label domains, which left every staging capacity request pending.
func TestCapacityRequestAvoidsKarpenterRestrictedLabels(t *testing.T) {
	pod, err := capacityPod(CloudJobConfig{Namespace: "cloud", NodeClassName: "sparkwing-jobs", RunnerImage: "runner", CPUCeiling: 4, MemoryCeiling: 8 << 30}, demandFor(teamAward("team")))
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for key := range pod.Spec.NodeSelector {
		keys = append(keys, key)
	}
	if affinity := pod.Spec.Affinity; affinity != nil && affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		for _, term := range affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			for _, expression := range term.MatchExpressions {
				keys = append(keys, expression.Key)
			}
		}
	}
	for _, key := range keys {
		if strings.HasPrefix(key, "karpenter.k8s.aws/") || strings.HasPrefix(key, "karpenter.sh/") {
			t.Fatalf("capacity request selects Karpenter-restricted label %s", key)
		}
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
