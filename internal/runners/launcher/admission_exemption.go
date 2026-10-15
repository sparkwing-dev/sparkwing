package launcher

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (l *Launcher) verifyAdmissionExemption(ctx context.Context) error {
	policy, err := l.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(ctx, AdmissionExemptionPolicy(l.Config.Namespace).Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	guard, err := l.Kube.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Get(ctx, policy.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if !equality.Semantic.DeepEqual(policy.Spec, AdmissionExemptionPolicy(l.Config.Namespace).Spec) || !equality.Semantic.DeepEqual(guard.Spec, AdmissionExemptionBinding(l.Config.Namespace).Spec) {
		return errors.New("Cloud admission exemption guard is missing or weakened")
	}
	probe := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "sparkwing-exemption-probe-" + strings.ToLower(rand.Text()), Namespace: l.Config.Namespace}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Default: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}}}}
	_, err = l.Kube.CoreV1().LimitRanges(l.Config.Namespace).Create(ctx, probe, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "ValidatingAdmissionPolicy '"+policy.Name+"' with binding '"+guard.Name+"' denied request:") {
		return errors.Join(errors.New("Cloud admission exemption guard did not enforce the dry-run denial"), err)
	}
	ns, err := l.Kube.CoreV1().Namespaces().Get(ctx, l.Config.Namespace, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("verify Cloud admission namespace: %w", err)
	}
	if ns.Labels["kubernetes.io/metadata.name"] != l.Config.Namespace {
		return errors.New("Cloud namespace is missing its protected name label")
	}
	hooks, err := l.Kube.AdmissionregistrationV1().MutatingWebhookConfigurations().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("verify Cloud admission exemption: %w", err)
	}
	for _, config := range hooks.Items {
		for _, hook := range config.Webhooks {
			if canonicalNamespaceExclusion(hook.NamespaceSelector, l.Config.Namespace) {
				continue
			}
			for _, rule := range hook.Rules {
				if mutatesCloud(rule) {
					return fmt.Errorf("Cloud namespace requires a protected name exclusion from mutating webhook %s", hook.Name)
				}
			}
		}
	}
	bindings, err := l.Kube.AdmissionregistrationV1().MutatingAdmissionPolicyBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, binding := range bindings.Items {
		if binding.Spec.MatchResources == nil || !canonicalNamespaceExclusion(binding.Spec.MatchResources.NamespaceSelector, l.Config.Namespace) {
			return fmt.Errorf("Cloud namespace requires a protected name exclusion from mutating policy binding %s", binding.Name)
		}
	}
	ranges, err := l.Kube.CoreV1().LimitRanges(l.Config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	if len(ranges.Items) != 0 {
		return errors.New("Cloud namespace must be exempt from LimitRange resource mutation")
	}
	return nil
}

func canonicalNamespaceExclusion(selector *metav1.LabelSelector, namespace string) bool {
	if selector == nil {
		return false
	}
	return slices.ContainsFunc(selector.MatchExpressions, func(e metav1.LabelSelectorRequirement) bool {
		return e.Key == "kubernetes.io/metadata.name" && e.Operator == metav1.LabelSelectorOpNotIn && slices.Contains(e.Values, namespace)
	})
}

func mutatesCloud(rule admissionv1.RuleWithOperations) bool {
	if rule.Scope != nil && *rule.Scope == admissionv1.ClusterScope {
		return false
	}
	changes := slices.ContainsFunc(rule.Operations, func(op admissionv1.OperationType) bool {
		return op == admissionv1.OperationAll || op == admissionv1.Create || op == admissionv1.Update
	})
	if !changes {
		return false
	}
	for _, group := range rule.APIGroups {
		for _, r := range rule.Resources {
			resource, _, _ := strings.Cut(r, "/")
			if (group == "*" || group == "batch") && (resource == "*" || resource == "jobs") {
				return true
			}
			if (group == "*" || group == "") && (resource == "*" || resource == "pods") {
				return true
			}
		}
	}
	return false
}
