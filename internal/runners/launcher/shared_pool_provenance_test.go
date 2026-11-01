package launcher

import (
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSharedPoolRefusesForeignProvenance(t *testing.T) {
	for _, change := range []string{"claim team", "pool team", "empty claim team", "empty pool team", "node warm namespace", "claim warm namespace", "pool warm namespace", "wrong node class", "wrong node class group", "wrong node class kind", "missing node class", "unconfigured node class"} {
		t.Run(change, func(t *testing.T) {
			b, kube := boundaryFixture()
			pool, err := b.Karpenter.Resource(nodePools).Get(t.Context(), "pool", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := b.Karpenter.Resource(nodeClaims).Get(t.Context(), "claim", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "claim team", "empty claim team":
				labels := claim.GetLabels()
				labels[TeamNodeLabel] = TeamNodeValue("cloud", "team")
				if change == "empty claim team" {
					labels[TeamNodeLabel] = ""
				}
				claim.SetLabels(labels)
			case "empty pool team":
				err = unstructured.SetNestedField(pool.Object, "", "spec", "template", "metadata", "labels", TeamNodeLabel)
			case "pool team":
				err = unstructured.SetNestedField(pool.Object, TeamNodeValue("cloud", "team"), "spec", "template", "metadata", "labels", TeamNodeLabel)
			case "node warm namespace":
				node, e := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
				if e != nil {
					t.Fatal(e)
				}
				node.Labels[WarmNamespaceLabel] = "foreign"
				_, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{})
			case "claim warm namespace":
				labels := claim.GetLabels()
				labels[WarmNamespaceLabel] = "foreign"
				claim.SetLabels(labels)
			case "pool warm namespace":
				err = unstructured.SetNestedField(pool.Object, "foreign", "spec", "template", "metadata", "labels", WarmNamespaceLabel)
			case "wrong node class":
				err = unstructured.SetNestedField(pool.Object, "unhardened", "spec", "template", "spec", "nodeClassRef", "name")
			case "wrong node class group":
				err = unstructured.SetNestedField(pool.Object, "other", "spec", "template", "spec", "nodeClassRef", "group")
			case "wrong node class kind":
				err = unstructured.SetNestedField(pool.Object, "OtherClass", "spec", "template", "spec", "nodeClassRef", "kind")
			case "missing node class":
				unstructured.RemoveNestedField(pool.Object, "spec", "template", "spec", "nodeClassRef")
			case "unconfigured node class":
				b.NodeClassName = ""
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = b.Karpenter.Resource(nodePools).Update(t.Context(), pool, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = b.Karpenter.Resource(nodeClaims).Update(t.Context(), claim, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = b.Reserve(t.Context(), boundaryBinding("team"), "job", CloudPlacement{RequestedClass: "2"}); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatalf("reserve: %v", err)
			}
			node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if reservationEvidence(node) || node.Labels[usedRunLabel] != "" {
				t.Fatal("refused node was bound")
			}
		})
	}
}

func TestSharedPoolRefusesPartialBinding(t *testing.T) {
	keys := []string{TeamNodeLabel, usedTeamLabel, usedRunLabel}
	for mask := 1; mask < 7; mask++ {
		t.Run(string(rune('0'+mask)), func(t *testing.T) {
			b, kube := boundaryFixture()
			node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for i, key := range keys {
				if mask&(1<<i) != 0 {
					node.Labels[key] = TeamNodeValue("cloud", "team")
					if key == usedRunLabel {
						node.Labels[key] = runNodeValue("cloud", "run")
					}
				}
			}
			if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = b.Reserve(t.Context(), boundaryBinding("team"), "job", CloudPlacement{RequestedClass: "2"}); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatalf("reserve: %v", err)
			}
		})
	}
}

func TestSharedPoolRefusesEmptyBindingLabels(t *testing.T) {
	for _, key := range []string{TeamNodeLabel, usedTeamLabel, usedRunLabel} {
		t.Run(key, func(t *testing.T) {
			b, kube := boundaryFixture()
			node, err := kube.CoreV1().Nodes().Get(t.Context(), "node", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			node.Labels[key] = ""
			if _, err = kube.CoreV1().Nodes().Update(t.Context(), node, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = b.Reserve(t.Context(), boundaryBinding("team"), "job", CloudPlacement{RequestedClass: "2"}); !errors.Is(err, ErrNoDedicatedNode) {
				t.Fatalf("reserve: %v", err)
			}
		})
	}
}
