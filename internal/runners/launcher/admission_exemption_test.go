package launcher

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func FuzzLauncherRequiresJobAndPodAdmissionExemption(f *testing.F) {
	for _, pod := range []bool{false, true} {
		for _, update := range []bool{false, true} {
			for _, exempt := range []bool{false, true} {
				f.Add(pod, update, exempt)
			}
		}
	}
	f.Fuzz(func(t *testing.T, pod, update, exempt bool) {
		l, kube, _, control := fixture()
		group, resource := "batch", "jobs"
		if pod {
			group, resource = "", "pods"
		}
		op := admissionv1.Create
		if update {
			op = admissionv1.Update
		}
		selector := &metav1.LabelSelector{}
		if exempt {
			selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"cloud"}}}
		}
		hook := &admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "injector"}, Webhooks: []admissionv1.MutatingWebhook{{Name: "injector.example", NamespaceSelector: selector, Rules: []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{op}, Rule: admissionv1.Rule{APIGroups: []string{group}, APIVersions: []string{"*"}, Resources: []string{resource}}}}}}}
		if _, err := kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(t.Context(), hook, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		_, err := l.LaunchOne(t.Context())
		if exempt {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || control.claims != 0 {
			t.Fatalf("mutable runnable %s admitted: claims=%d err=%v", resource, control.claims, err)
		}
	})
}

func TestLauncherRequiresAdmissionGuard(t *testing.T) {
	for _, alter := range []string{"missing", "skip", "audit", "limitrange"} {
		t.Run(alter, func(t *testing.T) {
			l, kube, _, control := fixture()
			switch alter {
			case "missing":
				if err := kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Delete(t.Context(), AdmissionExemptionPolicy("cloud").Name, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			case "skip":
				policy := AdmissionExemptionPolicy("cloud")
				policy.Spec.MatchConditions = []admissionv1.MatchCondition{{Name: "skip", Expression: "false"}}
				if _, err := kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Update(t.Context(), policy, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "audit":
				binding := AdmissionExemptionBinding("cloud")
				binding.Spec.ValidationActions = []admissionv1.ValidationAction{admissionv1.Audit}
				if _, err := kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Update(t.Context(), binding, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "limitrange":
				if _, err := kube.CoreV1().LimitRanges("cloud").Create(t.Context(), &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "cloud"}}, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := l.LaunchOne(t.Context()); err == nil || control.claims != 0 {
				t.Fatalf("unprotected claim: %d %v", control.claims, err)
			}
		})
	}
}

func TestLauncherRefusesMutatingAdmissionPolicies(t *testing.T) {
	for _, exempt := range []bool{false, true} {
		t.Run(fmt.Sprint(exempt), func(t *testing.T) {
			l, kube, _, control := fixture()
			policy := &admissionv1.MutatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "injector"}, Spec: admissionv1.MutatingAdmissionPolicySpec{MatchConstraints: &admissionv1.MatchResources{ResourceRules: []admissionv1.NamedRuleWithOperations{{RuleWithOperations: admissionv1.RuleWithOperations{Operations: []admissionv1.OperationType{admissionv1.Update}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"*"}, Resources: []string{"pods/ephemeralcontainers"}}}}}}}}
			binding := &admissionv1.MutatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: "injector"}, Spec: admissionv1.MutatingAdmissionPolicyBindingSpec{PolicyName: "injector"}}
			if exempt {
				binding.Spec.MatchResources = &admissionv1.MatchResources{NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"cloud"}}}}}
			}
			if _, err := kube.AdmissionregistrationV1().MutatingAdmissionPolicies().Create(t.Context(), policy, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := kube.AdmissionregistrationV1().MutatingAdmissionPolicyBindings().Create(t.Context(), binding, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			_, err := l.LaunchOne(t.Context())
			if exempt {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || control.claims != 0 {
				t.Fatalf("policy could mutate runnable Pod: %d %v", control.claims, err)
			}
		})
	}
}

func TestLauncherRefusesMutableNamespaceLabelExemption(t *testing.T) {
	for _, selector := range []*metav1.LabelSelector{
		{MatchLabels: map[string]string{"admission": "enabled"}},
		{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "admission", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"disabled"}}}},
	} {
		l, kube, _, control := fixture()
		ns, err := kube.CoreV1().Namespaces().Get(t.Context(), "cloud", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ns.Labels["admission"] = "disabled"
		if _, err = kube.CoreV1().Namespaces().Update(t.Context(), ns, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		hook := &admissionv1.MutatingWebhookConfiguration{ObjectMeta: metav1.ObjectMeta{Name: "mutable-exemption"}, Webhooks: []admissionv1.MutatingWebhook{{Name: "injector.example", NamespaceSelector: selector, Rules: []admissionv1.RuleWithOperations{{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"pods"}}}}}}}
		if _, err = kube.AdmissionregistrationV1().MutatingWebhookConfigurations().Create(t.Context(), hook, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err = l.LaunchOne(t.Context()); err == nil || control.claims != 0 {
			t.Fatalf("mutable namespace labels admitted a claim: claims=%d err=%v", control.claims, err)
		}
	}
}

func TestLauncherRefusesExistingPolicyBindingsWithoutProtectedExemption(t *testing.T) {
	for _, scope := range []string{"unrelated resources", "policy namespace exclusion", "unrelated binding resources"} {
		t.Run(scope, func(t *testing.T) {
			l, kube, _, control := fixture()
			policy := &admissionv1.MutatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "expandable"}, Spec: admissionv1.MutatingAdmissionPolicySpec{MatchConstraints: &admissionv1.MatchResources{ResourceRules: []admissionv1.NamedRuleWithOperations{{RuleWithOperations: admissionv1.RuleWithOperations{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"configmaps"}}}}}}}}
			binding := &admissionv1.MutatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: "expandable"}, Spec: admissionv1.MutatingAdmissionPolicyBindingSpec{PolicyName: policy.Name}}
			if scope == "policy namespace exclusion" {
				policy.Spec.MatchConstraints.ResourceRules[0].Resources = []string{"pods"}
				policy.Spec.MatchConstraints.NamespaceSelector = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "kubernetes.io/metadata.name", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"cloud"}}}}
			}
			if scope == "unrelated binding resources" {
				binding.Spec.MatchResources = &admissionv1.MatchResources{ResourceRules: []admissionv1.NamedRuleWithOperations{{RuleWithOperations: admissionv1.RuleWithOperations{Operations: []admissionv1.OperationType{admissionv1.Create}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"configmaps"}}}}}}
			}
			if _, err := kube.AdmissionregistrationV1().MutatingAdmissionPolicies().Create(t.Context(), policy, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := kube.AdmissionregistrationV1().MutatingAdmissionPolicyBindings().Create(t.Context(), binding, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := l.LaunchOne(t.Context()); err == nil || control.claims != 0 {
				t.Fatalf("expandable policy binding admitted a claim: claims=%d err=%v", control.claims, err)
			}
		})
	}
}

func TestLauncherProbesAdmissionGuardEnforcement(t *testing.T) {
	l, kube, _, control := fixture()
	kube.PrependReactor("create", "limitranges", func(action ktesting.Action) (bool, runtime.Object, error) {
		if len(action.(ktesting.CreateActionImpl).CreateOptions.DryRun) != 1 {
			t.Fatal("enforcement probe could persist a LimitRange")
		}
		return true, action.(ktesting.CreateAction).GetObject(), nil
	})
	if _, err := l.LaunchOne(t.Context()); err == nil || control.claims != 0 {
		t.Fatalf("unenforced guard accepted: claims=%d err=%v", control.claims, err)
	}
}
