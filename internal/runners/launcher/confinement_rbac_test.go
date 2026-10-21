package launcher

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

type confinementChart struct {
	resources  []confinementResource
	identities []string
	verbs      []string
	namespaces []string
}

func (chart *confinementChart) enrollNamespace(object *unstructured.Unstructured) {
	if namespace := object.GetNamespace(); namespace != "" {
		chart.namespaces = append(chart.namespaces, namespace)
	}
	if object.GetKind() == "Namespace" {
		chart.namespaces = append(chart.namespaces, object.GetName())
	}
}

func TestConfinementEnrollsRenderedNamespaces(t *testing.T) {
	var chart confinementChart
	for _, object := range []*unstructured.Unstructured{
		{Object: map[string]interface{}{"kind": "Namespace", "metadata": map[string]interface{}{"name": "extra"}}},
		{Object: map[string]interface{}{"kind": "RoleBinding", "metadata": map[string]interface{}{"namespace": "bound"}}},
	} {
		chart.enrollNamespace(object)
	}
	for _, namespace := range []string{"extra", "bound"} {
		if !slices.Contains(chart.namespaces, namespace) {
			t.Errorf("rendered namespace %q excluded from authorization matrix", namespace)
		}
	}
}

type confinementResource struct {
	group, resource, name string
	cluster               bool
}

// The API server evaluates the installed chart's combined grants, including grants
// outside the launcher template. Network enforcement needs a separate CNI-backed test.
func TestCompleteChartRBACConfinement(t *testing.T) {
	for _, chart := range []string{"sparkwing-runner-bundle", "sparkwing-full"} {
		t.Run(chart, func(t *testing.T) {
			cfg := apiServerAuthorizing(t, "RBAC")
			granted := installConfinementChart(t, cfg, chart)
			resources := []confinementResource{
				{"", "pods", "job", false},
				{"batch", "jobs", "job", false},
				{"", "secrets", "credential", false},
				{"networking.k8s.io", "networkpolicies", "boundary", false},
				{"discovery.k8s.io", "endpointslices", "kubernetes", false},
				{"", "serviceaccounts", "sparkwing-customer-job", false},
				{"", "serviceaccounts/token", "sparkwing-customer-job", false},
				{"rbac.authorization.k8s.io", "roles", "launcher", false},
				{"rbac.authorization.k8s.io", "rolebindings", "launcher", false},
				{"", "pods/exec", "job", false},
				{"", "pods/attach", "job", false},
				{"", "pods/log", "job", false},
				{"", "nodes", "worker", true},
				{"karpenter.sh", "nodeclaims", "worker", true},
				{"karpenter.sh", "nodepools", "pool", true},
				{"rbac.authorization.k8s.io", "clusterroles", "launcher", true},
				{"rbac.authorization.k8s.io", "clusterrolebindings", "launcher", true},
				{"admissionregistration.k8s.io", "validatingadmissionpolicies", "sparkwing-cloud-pods-cloud-cloud-jobs", true},
				{"admissionregistration.k8s.io", "validatingadmissionpolicybindings", "sparkwing-cloud-jobs-cloud-cloud-jobs", true},
				{"admissionregistration.k8s.io", "validatingadmissionpolicies", "foreign-policy", true},
			}
			resources = append(resources, granted.resources...)
			identities := append(granted.identities, []string{"system:serviceaccount:cloud:cloud-launcher", "system:serviceaccount:cloud-cloud-jobs:sparkwing-customer-job", "system:serviceaccount:cloud:controller", "system:serviceaccount:foreign:cloud-launcher"}...)
			slices.Sort(identities)
			identities = slices.Compact(identities)
			verbs := append(granted.verbs, []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection", "bind", "escalate", "impersonate"}...)
			slices.Sort(verbs)
			verbs = slices.Compact(verbs)
			for _, identity := range identities {
				client := impersonate(t, cfg, identity)
				for _, resource := range resources {
					namespaces := append([]string{"cloud-cloud-jobs", "cloud", "foreign", "default", ""}, granted.namespaces...)
					slices.Sort(namespaces)
					namespaces = slices.Compact(namespaces)
					if resource.cluster {
						namespaces = []string{""}
					}
					for _, namespace := range namespaces {
						for _, verb := range verbs {
							t.Run(fmt.Sprintf("%s/%s/%s/%s/%s", identity, namespace, resource.resource, resource.name, verb), func(t *testing.T) {
								result, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(t.Context(), &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: namespace, Verb: verb, Group: resource.group, Resource: strings.Split(resource.resource, "/")[0], Subresource: subresource(resource.resource), Name: resource.name}}}, metav1.CreateOptions{})
								if err != nil {
									t.Fatal(err)
								}
								want := identity == "system:serviceaccount:cloud:cloud-launcher" && chartLauncherGrant(resource.group, resource.resource, resource.name, namespace, verb)
								if result.Status.Allowed != want || result.Status.EvaluationError != "" {
									t.Fatalf("allowed=%v, want %v: %+v", result.Status.Allowed, want, result.Status)
								}
							})
						}
					}
				}
			}
		})
	}
}

func subresource(resource string) string {
	_, sub, _ := strings.Cut(resource, "/")
	return sub
}

func chartLauncherGrant(group, resource, name, namespace, verb string) bool {
	expectedGroups := map[string]string{"jobs": "batch", "networkpolicies": "networking.k8s.io", "endpointslices": "discovery.k8s.io", "nodeclaims": "karpenter.sh", "nodepools": "karpenter.sh", "validatingadmissionpolicies": "admissionregistration.k8s.io", "validatingadmissionpolicybindings": "admissionregistration.k8s.io"}
	if group != expectedGroups[resource] {
		return false
	}
	verbs := ""
	switch resource {
	case "pods":
		if verb == "list" {
			return true
		}
		if namespace == "cloud-cloud-jobs" {
			verbs = "create get delete"
		}
	case "jobs":
		if namespace == "cloud-cloud-jobs" {
			verbs = "create get list patch delete"
		}
	case "secrets":
		if namespace == "cloud-cloud-jobs" {
			verbs = "create delete"
		}
	case "networkpolicies":
		if namespace == "cloud-cloud-jobs" {
			verbs = "list create delete"
		}
	case "endpointslices":
		if namespace == "default" {
			verbs = "list"
		}
	case "nodes":
		verbs = "get list update patch"
	case "nodeclaims":
		verbs = "get delete"
	case "nodepools":
		verbs = "get"
	case "validatingadmissionpolicies", "validatingadmissionpolicybindings":
		if name == "sparkwing-cloud-pods-cloud-cloud-jobs" || name == "sparkwing-cloud-jobs-cloud-cloud-jobs" {
			verbs = "get"
		}
	}
	return slices.Contains(strings.Fields(verbs), verb)
}

func installConfinementChart(t *testing.T, cfg *rest.Config, chart string) confinementChart {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal("helm is required to exercise complete chart RBAC")
	}
	prefix := ""
	args := []string{"template", "sparkwing", filepath.Join("..", "..", "..", "charts", chart), "--namespace", "cloud"}
	if chart == "sparkwing-full" {
		prefix = "sparkwing-runner-bundle."
		args = append(args, "--set", "controller.image.repository=registry.example/controller", "--set", "controller.image.tag=test", "--set", "serviceAccount.name=controller")
	}
	for _, setting := range []string{"launcher.enabled=true", "launcher.serviceAccountName=cloud-launcher", "launcher.image.repository=registry.example/launcher", "launcher.image.tag=test", "launcher.cpuCeiling=8", "launcher.memoryCeiling=16Gi", "launcher.tokenSecret.name=launcher-token", "controller.url=https://controller.example", "launcher.jobImage.repository=registry.example/runner", "launcher.jobImage.tag=test", "launcher.dockerImage.repository=registry.example/docker", "launcher.dockerImage.tag=dind", "launcher.jobImage.digest=sha256:" + strings.Repeat("a", 64), "launcher.dockerImage.digest=sha256:" + strings.Repeat("b", 64)} {
		args = append(args, "--set", prefix+setting)
	}
	rendered, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("render complete %s: %v\n%s", chart, err, rendered)
	}
	admin := impersonate(t, cfg, "")
	for _, namespace := range []string{"cloud", "foreign", "default"} {
		if _, err := admin.CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		t.Fatal(err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groups)
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(rendered), 4096)
	installed := 0
	var granted confinementChart
	var bindings []*unstructured.Unstructured
	for {
		object := &unstructured.Unstructured{}
		if err := decoder.Decode(&object.Object); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(object.Object) == 0 {
			continue
		}
		mapping, err := mapper.RESTMapping(object.GroupVersionKind().GroupKind(), object.GroupVersionKind().Version)
		if err != nil {
			t.Fatal(err)
		}
		var target dynamic.ResourceInterface = client.Resource(mapping.Resource)
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			if object.GetNamespace() == "" {
				object.SetNamespace("cloud")
			}
			target = client.Resource(mapping.Resource).Namespace(object.GetNamespace())
		}
		if _, err := target.Create(t.Context(), object, metav1.CreateOptions{}); err != nil {
			t.Fatalf("install %s %s/%s: %v", object.GetKind(), object.GetNamespace(), object.GetName(), err)
		}
		granted.enrollNamespace(object)
		// Enroll every rendered resource grant so a new template cannot escape the matrix.
		if object.GetKind() == "Role" || object.GetKind() == "ClusterRole" {
			rules, _, err := unstructured.NestedSlice(object.Object, "rules")
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range rules {
				rule := raw.(map[string]interface{})
				urls, _, _ := unstructured.NestedStringSlice(rule, "nonResourceURLs")
				if len(urls) != 0 {
					t.Fatalf("chart grants unexpected non-resource authority: %v", urls)
				}
				verbs, _, _ := unstructured.NestedStringSlice(rule, "verbs")
				granted.verbs = append(granted.verbs, verbs...)
				groups, _, _ := unstructured.NestedStringSlice(rule, "apiGroups")
				resources, _, _ := unstructured.NestedStringSlice(rule, "resources")
				names, _, _ := unstructured.NestedStringSlice(rule, "resourceNames")
				names = append(names, "ungranted-name")
				for _, group := range groups {
					for _, resource := range resources {
						for _, name := range names {
							granted.resources = append(granted.resources, confinementResource{group, resource, name, false})
						}
					}
				}
			}
		}
		if object.GetKind() == "RoleBinding" || object.GetKind() == "ClusterRoleBinding" {
			bindings = append(bindings, object.DeepCopy())
			subjects, _, err := unstructured.NestedSlice(object.Object, "subjects")
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range subjects {
				subject := raw.(map[string]interface{})
				kind, _, _ := unstructured.NestedString(subject, "kind")
				name, _, _ := unstructured.NestedString(subject, "name")
				namespace, _, _ := unstructured.NestedString(subject, "namespace")
				switch kind {
				case "ServiceAccount":
					granted.identities = append(granted.identities, "system:serviceaccount:"+namespace+":"+name)
				case "User":
					granted.identities = append(granted.identities, name)
				default:
					t.Fatalf("chart grants unexpected %s authority to %s", kind, name)
				}
			}
		}

		installed++
	}
	if installed == 0 {
		t.Fatal("complete chart rendered no resources")
	}
	slices.Sort(granted.verbs)
	granted.verbs = slices.Compact(granted.verbs)
	slices.Sort(granted.identities)
	granted.identities = slices.Compact(granted.identities)
	waitForChartRBAC(t, cfg, bindings)
	return granted
}

// Chart-derived probes synchronize the authorizer cache; contract expectations
// remain independent so extra grants still fail the confinement matrix.
func waitForChartRBAC(t *testing.T, cfg *rest.Config, bindings []*unstructured.Unstructured) {
	t.Helper()
	type probe struct {
		identity   string
		attributes authorizationv1.ResourceAttributes
	}
	var probes []probe
	admin := impersonate(t, cfg, "")
	for _, binding := range bindings {
		kind, _, _ := unstructured.NestedString(binding.Object, "roleRef", "kind")
		name, _, _ := unstructured.NestedString(binding.Object, "roleRef", "name")
		var rules []rbacv1.PolicyRule
		switch kind {
		case "Role":
			role, err := admin.RbacV1().Roles(binding.GetNamespace()).Get(t.Context(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			rules = role.Rules
		case "ClusterRole":
			role, err := admin.RbacV1().ClusterRoles().Get(t.Context(), name, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			rules = role.Rules
		default:
			t.Fatalf("unknown roleRef kind %s", kind)
		}
		subjects, _, err := unstructured.NestedSlice(binding.Object, "subjects")
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range subjects {
			subject := raw.(map[string]interface{})
			identity, _, _ := unstructured.NestedString(subject, "name")
			kind, _, _ := unstructured.NestedString(subject, "kind")
			if kind == "ServiceAccount" {
				namespace, _, _ := unstructured.NestedString(subject, "namespace")
				identity = "system:serviceaccount:" + namespace + ":" + identity
			}
			for _, rule := range rules {
				names := rule.ResourceNames
				if len(names) == 0 {
					names = []string{"ungranted-name"}
				}
				for _, group := range rule.APIGroups {
					for _, resource := range rule.Resources {
						for _, verb := range rule.Verbs {
							for _, name := range names {
								resource, sub, _ := strings.Cut(resource, "/")
								probes = append(probes, probe{identity, authorizationv1.ResourceAttributes{Namespace: binding.GetNamespace(), Group: group, Resource: resource, Subresource: sub, Verb: verb, Name: name}})
							}
						}
					}
				}
			}
		}
	}
	err := wait.PollUntilContextTimeout(t.Context(), 10*time.Millisecond, time.Minute, true, func(ctx context.Context) (bool, error) {
		for _, probe := range probes {
			client := impersonate(t, cfg, probe.identity)
			review, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &probe.attributes}}, metav1.CreateOptions{})
			if err != nil {
				return false, err
			}
			if !review.Status.Allowed {
				return false, nil
			}
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("complete chart RBAC not ready: %v", err)
	}
}
