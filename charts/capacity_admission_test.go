package charts

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/cel"
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	celconfig "k8s.io/apiserver/pkg/apis/cel"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/cel/environment"
	"sigs.k8s.io/yaml"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func TestLauncherCapacityAdmission(t *testing.T) {
	for chart, prefix := range map[string]string{"./sparkwing-runner-bundle": "", "./sparkwing-full": "sparkwing-runner-bundle."} {
		t.Run(chart, func(t *testing.T) {
			settings := append(launcherImages(prefix), prefix+"launcher.serviceAccountName=cloud-launcher", prefix+"launcher.jobNamespace=customer-jobs")
			var policy admissionv1.ValidatingAdmissionPolicy
			var binding admissionv1.ValidatingAdmissionPolicyBinding
			var account corev1.ServiceAccount
			for _, raw := range strings.Split(helmRenderAll(t, chart, "sparkwing", "cloud", settings...), "\n---") {
				var doc renderedResource
				if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
					t.Fatal(err)
				}
				switch {
				case doc.Kind == "ValidatingAdmissionPolicy" && doc.Metadata.Name == "sparkwing-cloud-capacity-customer-jobs":
					if err := yaml.Unmarshal([]byte(raw), &policy); err != nil {
						t.Fatal(err)
					}
				case doc.Kind == "ValidatingAdmissionPolicyBinding" && doc.Metadata.Name == "sparkwing-cloud-capacity-customer-jobs":
					if err := yaml.Unmarshal([]byte(raw), &binding); err != nil {
						t.Fatal(err)
					}
				case doc.Kind == "ServiceAccount" && doc.Metadata.Name == "cloud-launcher":
					if err := yaml.Unmarshal([]byte(raw), &account); err != nil {
						t.Fatal(err)
					}
				}
			}
			if account.AutomountServiceAccountToken == nil || *account.AutomountServiceAccountToken {
				t.Fatal("launcher ServiceAccount must disable token automount")
			}
			if policy.Name == "" || policy.Spec.FailurePolicy == nil || *policy.Spec.FailurePolicy != admissionv1.Fail {
				t.Fatal("capacity policy must fail closed")
			}
			if binding.Spec.PolicyName != policy.Name || !reflect.DeepEqual(binding.Spec.ValidationActions, []admissionv1.ValidationAction{admissionv1.Deny}) || binding.Spec.MatchResources != nil {
				t.Fatalf("capacity binding must enforce the policy cluster-wide: %+v", binding.Spec)
			}
			cluster := admissionv1.ClusterScope
			wantRules := []admissionv1.NamedRuleWithOperations{
				{RuleWithOperations: admissionv1.RuleWithOperations{Operations: []admissionv1.OperationType{admissionv1.Update}, Rule: admissionv1.Rule{APIGroups: []string{""}, APIVersions: []string{"v1"}, Resources: []string{"nodes"}, Scope: &cluster}}},
				{RuleWithOperations: admissionv1.RuleWithOperations{Operations: []admissionv1.OperationType{admissionv1.Delete}, Rule: admissionv1.Rule{APIGroups: []string{"karpenter.sh"}, APIVersions: []string{"v1"}, Resources: []string{"nodeclaims"}, Scope: &cluster}}},
			}
			if !reflect.DeepEqual(policy.Spec.MatchConstraints.ResourceRules, wantRules) || len(policy.Spec.MatchConditions) != 0 || len(policy.Spec.MatchConstraints.ExcludeResourceRules) != 0 || policy.Spec.MatchConstraints.ObjectSelector != nil {
				t.Fatalf("capacity writes must match without a label selector bypass: %+v", policy.Spec)
			}
			var classes []string
			for _, v := range policy.Spec.Variables {
				if v.Name == "cloudClasses" {
					if err := yaml.Unmarshal([]byte(v.Expression), &classes); err != nil {
						t.Fatal(err)
					}
				}
			}
			var wantClasses []string
			for _, size := range match.CloudSizes() {
				wantClasses = append(wantClasses, strconv.Itoa(int(size.Resources.Cores)))
			}
			if !reflect.DeepEqual(classes, wantClasses) {
				t.Fatalf("capacity policy classes = %v, want CloudSizes %v", classes, wantClasses)
			}
			testCapacityPolicyCEL(t, &policy)
		})
	}
}

func testCapacityPolicyCEL(t *testing.T, policy *admissionv1.ValidatingAdmissionPolicy) {
	t.Helper()
	compiler, err := cel.NewCompositedCompiler(environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion()))
	if err != nil {
		t.Fatal(err)
	}
	options := cel.OptionalVariableDeclarations{}
	var variables []cel.NamedExpressionAccessor
	for _, v := range policy.Spec.Variables {
		variables = append(variables, &validating.Variable{Name: v.Name, Expression: v.Expression})
	}
	compiler.CompileAndStoreVariables(variables, options, environment.StoredExpressions)
	var expressions []cel.ExpressionAccessor
	for _, v := range policy.Spec.Validations {
		expressions = append(expressions, &validating.ValidationCondition{Expression: v.Expression, Message: v.Message})
	}
	none := compiler.CompileCondition(nil, options, environment.StoredExpressions)
	validator := validating.NewValidator(compiler.CompileCondition(expressions, options, environment.StoredExpressions), nil, none, none, policy.Spec.FailurePolicy, nil)
	if err := validator.CompileError(); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{launcher.PoolNamespaceLabel: "customer-jobs", launcher.CloudClassLabel: "2", launcher.WarmNamespaceLabel: "cloud-warm", "karpenter.sh/nodepool": "operator-pool", corev1.LabelHostname: "fresh", corev1.LabelTopologyZone: "us-west-2a"}
	fresh := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "fresh", Labels: labels}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{
		{Key: launcher.DedicatedTaint, Value: "true", Effect: corev1.TaintEffectNoSchedule},
		{Key: "example.com/operator", Value: "held", Effect: corev1.TaintEffectNoSchedule},
	}}}
	bound := fresh.DeepCopy()
	bound.Labels[launcher.TeamNodeLabel] = launcher.TeamNodeValue("customer-jobs", "team")
	bound.Labels["node-restriction.kubernetes.io/sparkwing-used-team"] = bound.Labels[launcher.TeamNodeLabel]
	bound.Labels["node-restriction.kubernetes.io/sparkwing-used-run"] = "run-first"
	bound.Annotations = map[string]string{"sparkwing.dev/node-reservation": "reservation", "sparkwing.dev/reservation-namespace": "customer-jobs", "sparkwing.dev/award-binding": "binding", "sparkwing.dev/used-run": "run"}
	bound.Spec.Taints = append(bound.Spec.Taints, corev1.Taint{Key: "sparkwing.dev/cloud-reserved", Value: "reservation", Effect: corev1.TaintEffectNoSchedule})
	claim := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "karpenter.sh/v1", "kind": "NodeClaim", "metadata": map[string]any{"name": "claim"}}}
	claim.SetLabels(labels)
	const identity = "system:serviceaccount:cloud:cloud-launcher"
	check := func(t *testing.T, old, next runtime.Object, actor string, want bool) {
		t.Helper()
		gvk, gvr, operation := corev1.SchemeGroupVersion.WithKind("Node"), corev1.SchemeGroupVersion.WithResource("nodes"), admission.Update
		if _, ok := old.(*unstructured.Unstructured); ok {
			gvk, gvr, operation = schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodeClaim"}, schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}, admission.Delete
		}
		attrs := admission.NewAttributesRecord(next, old, gvk, "", "capacity", gvr, "", operation, nil, false, &user.DefaultInfo{Name: actor})
		versioned := &admission.VersionedAttributes{Attributes: attrs, VersionedKind: gvk, VersionedObject: next, VersionedOldObject: old}
		result := validator.Validate(t.Context(), gvr, versioned, nil, nil, celconfig.RuntimeCELCostBudget, nil)
		allowed := true
		for _, decision := range result.Decisions {
			if decision.Action == validating.ActionDeny {
				allowed = false
			}
		}
		if allowed != want {
			t.Fatalf("admitted=%v, want %v: %+v", allowed, want, result)
		}
	}
	for name, mutate := range map[string]func(*corev1.Node){
		"release": func(n *corev1.Node) {
			delete(n.Annotations, "sparkwing.dev/node-reservation")
			delete(n.Annotations, "sparkwing.dev/reservation-namespace")
			delete(n.Annotations, "sparkwing.dev/award-binding")
			n.Spec.Taints = slices.DeleteFunc(n.Spec.Taints, func(taint corev1.Taint) bool { return taint.Key == "sparkwing.dev/cloud-reserved" })
		},
		"quarantine": func(n *corev1.Node) {
			n.Annotations["sparkwing.dev/quarantined"] = "mismatch"
			n.Spec.Unschedulable = true
			n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: "sparkwing.dev/quarantined", Effect: corev1.TaintEffectNoSchedule})
		},
		"retire": func(n *corev1.Node) {
			n.Spec.Unschedulable = true
			n.Annotations["sparkwing.dev/retirement-cordon"] = "true"
		},
		"uncordon": func(n *corev1.Node) {
			n.Spec.Unschedulable = false
			delete(n.Annotations, "sparkwing.dev/retirement-cordon")
		},
	} {
		t.Run(name, func(t *testing.T) {
			old := bound.DeepCopy()
			if name == "uncordon" {
				old.Spec.Unschedulable = true
				old.Annotations["sparkwing.dev/retirement-cordon"] = "true"
			}
			next := old.DeepCopy()
			mutate(next)
			check(t, old, next, identity, true)
		})
	}
	t.Run("first reservation adds protected team and run labels", func(t *testing.T) { check(t, fresh, bound, identity, true) })
	t.Run("owned NodeClaim deletion with null object", func(t *testing.T) { check(t, claim, nil, identity, true) })
	for _, key := range []string{launcher.PoolNamespaceLabel, launcher.CloudClassLabel, "karpenter.sh/nodepool"} {
		values := []string{"", "foreign"}
		if key == "karpenter.sh/nodepool" {
			values = []string{""}
		}
		for _, value := range values {
			t.Run("foreign node/"+key+"/"+value, func(t *testing.T) {
				old := fresh.DeepCopy()
				old.Labels[key] = value
				check(t, old, bound, identity, false)
				check(t, old, bound, "admin", true)
			})
			t.Run("foreign NodeClaim/"+key+"/"+value, func(t *testing.T) {
				old := claim.DeepCopy()
				ls := old.GetLabels()
				ls[key] = value
				old.SetLabels(ls)
				check(t, old, nil, identity, false)
				check(t, old, nil, "system:serviceaccount:kube-system:karpenter", true)
			})
		}
	}
	for _, key := range []string{launcher.PoolNamespaceLabel, launcher.CloudClassLabel, launcher.WarmNamespaceLabel, "karpenter.sh/nodepool", launcher.NodeClassLabel} {
		t.Run("preserve provenance/"+key, func(t *testing.T) {
			next := bound.DeepCopy()
			next.Labels[key] = "foreign"
			check(t, bound, next, identity, false)
		})
	}
	for name, mutate := range map[string]func(*corev1.Node){
		"strip dedicated taint": func(n *corev1.Node) {
			n.Spec.Taints = slices.DeleteFunc(n.Spec.Taints, func(taint corev1.Taint) bool { return taint.Key == launcher.DedicatedTaint })
		},
		"change dedicated taint": func(n *corev1.Node) { n.Spec.Taints[0].Value = "false" },
		"add arbitrary label":    func(n *corev1.Node) { n.Labels["example.com/placement"] = "foreign" },
		"change topology label":  func(n *corev1.Node) { n.Labels[corev1.LabelTopologyZone] = "us-west-2b" },
		"change hostname label":  func(n *corev1.Node) { n.Labels[corev1.LabelHostname] = "foreign" },
		"remove hostname label":  func(n *corev1.Node) { delete(n.Labels, corev1.LabelHostname) },
		"add foreign taint": func(n *corev1.Node) {
			n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: "example.com/foreign", Effect: corev1.TaintEffectNoSchedule})
		},
		"remove foreign taint": func(n *corev1.Node) {
			n.Spec.Taints = slices.DeleteFunc(n.Spec.Taints, func(taint corev1.Taint) bool { return taint.Key == "example.com/operator" })
		},
		"change foreign taint":      func(n *corev1.Node) { n.Spec.Taints[1].Effect = corev1.TaintEffectNoExecute },
		"replace reservation taint": func(n *corev1.Node) { n.Spec.Taints[2].Value = "another-reservation" },
		"add second reservation taint": func(n *corev1.Node) {
			n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: "sparkwing.dev/cloud-reserved", Value: "another-reservation", Effect: corev1.TaintEffectNoSchedule})
		},
		"add owner reference": func(n *corev1.Node) {
			n.OwnerReferences = append(n.OwnerReferences, metav1.OwnerReference{APIVersion: "karpenter.sh/v1", Kind: "NodeClaim", Name: "missing", UID: "nonexistent"})
		},
		"add finalizer": func(n *corev1.Node) { n.Finalizers = append(n.Finalizers, "example.com/held") },
		"quarantine with eviction": func(n *corev1.Node) {
			n.Spec.Taints = append(n.Spec.Taints, corev1.Taint{Key: "sparkwing.dev/quarantined", Effect: corev1.TaintEffectNoExecute})
		},
	} {
		t.Run(name, func(t *testing.T) {
			next := bound.DeepCopy()
			mutate(next)
			check(t, bound, next, identity, false)
		})
	}
	for _, key := range []string{launcher.TeamNodeLabel, "node-restriction.kubernetes.io/sparkwing-used-team", "node-restriction.kubernetes.io/sparkwing-used-run"} {
		t.Run("preserve binding/"+key, func(t *testing.T) {
			next := bound.DeepCopy()
			next.Labels[key] = "another-binding"
			check(t, bound, next, identity, false)
			delete(next.Labels, key)
			check(t, bound, next, identity, false)
		})
	}
	t.Run("reservation with eviction", func(t *testing.T) {
		next := fresh.DeepCopy()
		next.Spec.Taints = append(next.Spec.Taints, corev1.Taint{Key: "sparkwing.dev/cloud-reserved", Value: "reservation", Effect: corev1.TaintEffectNoExecute})
		check(t, fresh, next, identity, false)
	})
	t.Run("preserve quarantine", func(t *testing.T) {
		old := bound.DeepCopy()
		old.Spec.Taints = append(old.Spec.Taints, corev1.Taint{Key: "sparkwing.dev/quarantined", Effect: corev1.TaintEffectNoSchedule})
		check(t, old, bound, identity, false)
		next := old.DeepCopy()
		next.Annotations["sparkwing.dev/quarantined"] = "another fault"
		check(t, old, next, identity, true)
		next.Spec.Taints[len(next.Spec.Taints)-1].Value = "changed"
		check(t, old, next, identity, false)
	})
	t.Run("unlabeled node cannot self claim", func(t *testing.T) { old := fresh.DeepCopy(); old.Labels = nil; check(t, old, bound, identity, false) })
	t.Run("unlabeled claim", func(t *testing.T) { old := claim.DeepCopy(); old.SetLabels(nil); check(t, old, nil, identity, false) })
	t.Run("team pool claim", func(t *testing.T) {
		old := claim.DeepCopy()
		ls := old.GetLabels()
		ls[launcher.TeamNodeLabel] = "team"
		old.SetLabels(ls)
		check(t, old, nil, identity, false)
	})
	t.Run("missing stored node", func(t *testing.T) { check(t, nil, bound, identity, false) })
	t.Run("other launcher cannot adopt foreign capacity", func(t *testing.T) {
		old := fresh.DeepCopy()
		old.Labels[launcher.PoolNamespaceLabel] = "other-jobs"
		check(t, old, bound, identity, false)
	})
}
