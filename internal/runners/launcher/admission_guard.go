package launcher

import (
	"fmt"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AdmissionExemptionPolicy preserves a Cloud namespace's verified mutator exemption.
func AdmissionExemptionPolicy(namespace string) *admissionv1.ValidatingAdmissionPolicy {
	excluded := fmt.Sprintf("has(s.matchExpressions) && s.matchExpressions.exists(e, e.key == 'kubernetes.io/metadata.name' && e.operator == 'NotIn' && %q in e.values)", namespace)
	webhookExclusion := "has(w.namespaceSelector) && [w.namespaceSelector].exists(s, " + excluded + ")"
	bindingExclusion := "has(object.spec.matchResources) && has(object.spec.matchResources.namespaceSelector) && [object.spec.matchResources.namespaceSelector].exists(s, " + excluded + ")"
	touches := "w.rules.exists(r, r.operations.exists(o, o in ['*', 'CREATE', 'UPDATE']) && r.apiGroups.exists(g, g in ['', 'batch', '*']) && r.resources.exists(x, x.split('/')[0] in ['*', 'jobs', 'pods']))"
	return &admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "sparkwing-cloud-exemption-" + namespace}, Spec: admissionv1.ValidatingAdmissionPolicySpec{
		FailurePolicy: ptr(admissionv1.Fail), MatchConstraints: &admissionv1.MatchResources{MatchPolicy: ptr(admissionv1.Equivalent), NamespaceSelector: &metav1.LabelSelector{}, ObjectSelector: &metav1.LabelSelector{}, ResourceRules: []admissionv1.NamedRuleWithOperations{{RuleWithOperations: admissionv1.RuleWithOperations{Operations: []admissionv1.OperationType{admissionv1.Create, admissionv1.Update}, Rule: admissionv1.Rule{Scope: ptr(admissionv1.AllScopes), APIGroups: []string{"", "admissionregistration.k8s.io"}, APIVersions: []string{"*"}, Resources: []string{"namespaces", "namespaces/*", "limitranges", "mutatingwebhookconfigurations", "mutatingadmissionpolicybindings"}}}}}},
		Validations: []admissionv1.Validation{
			{Expression: fmt.Sprintf("request.resource.resource != 'namespaces' || object.metadata.name != %q || (has(object.metadata.labels) && 'kubernetes.io/metadata.name' in object.metadata.labels && object.metadata.labels['kubernetes.io/metadata.name'] == %q)", namespace, namespace)},
			{Expression: fmt.Sprintf("request.resource.resource != 'limitranges' || request.namespace != %q", namespace)},
			{Expression: "request.resource.resource != 'mutatingwebhookconfigurations' || object.webhooks.all(w, !has(w.rules) || !(" + touches + ") || (" + webhookExclusion + "))"},
			{Expression: "request.resource.resource != 'mutatingadmissionpolicybindings' || (" + bindingExclusion + ")"},
		},
	}}
}

// AdmissionExemptionBinding enforces the Cloud exemption policy with denial on failure.
func AdmissionExemptionBinding(namespace string) *admissionv1.ValidatingAdmissionPolicyBinding {
	return &admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: "sparkwing-cloud-exemption-" + namespace}, Spec: admissionv1.ValidatingAdmissionPolicyBindingSpec{PolicyName: "sparkwing-cloud-exemption-" + namespace, ValidationActions: []admissionv1.ValidationAction{admissionv1.Deny}}}
}
