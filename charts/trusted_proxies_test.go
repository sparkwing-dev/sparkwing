package charts

import (
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestControllerTrustedProxyCIDRs(t *testing.T) {
	for _, configured := range []bool{false, true} {
		var sets []string
		if configured {
			sets = []string{"controller.trustedProxyCIDRs[0]=10.0.0.0/8", "controller.trustedProxyCIDRs[1]=2001:db8:ffff::/48"}
		}
		resources := renderedResources(t, helmRender(t, "./sparkwing-full", "templates/controller-deployment.yaml", "sparkwing", sets...))
		deployment := componentResource(t, resources, "Deployment", "controller")
		_, got := hasFlag(resourceContainer(t, deployment).Args, "--trusted-proxy-cidrs=")
		if got != configured {
			t.Fatalf("proxy flag presence = %t, want %t", got, configured)
		}
		if configured && !containsArg(resourceContainer(t, deployment).Args, "--trusted-proxy-cidrs=10.0.0.0/8,2001:db8:ffff::/48") {
			t.Fatal("CIDRs missing from controller arguments")
		}
	}
}

func typedChartObjects(t *testing.T, manifest string) map[string][]json.RawMessage {
	t.Helper()
	decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	out := map[string][]json.RawMessage{}
	for {
		var raw runtime.RawExtension
		if err := decoder.Decode(&raw); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		var meta struct{ Kind string }
		if err := json.Unmarshal(raw.Raw, &meta); err != nil {
			t.Fatal(err)
		}
		out[meta.Kind] = append(out[meta.Kind], raw.Raw)
	}
}

func TestTrustedProxyListenerAdmitsOnlyTheIngressNamespace(t *testing.T) {
	manifest := helmRenderAll(t, "./sparkwing-full", "sparkwing", "sparkwing",
		"ingress.enabled=true", "ingress.allowInsecure=true", "ingress.hosts[0].host=api.example.test", "ingress.hosts[0].paths[0].path=/",
		"controller.trustedProxyListener.enabled=true",
		"controller.trustedProxyListener.namespaceSelector.kubernetes\\.io/metadata\\.name=ingress-nginx")
	objects := typedChartObjects(t, manifest)
	var deployment appsv1.Deployment
	for _, raw := range objects["Deployment"] {
		var d appsv1.Deployment
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if d.Labels["app.kubernetes.io/component"] == "controller" {
			deployment = d
		}
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	if !slices.Contains(container.Args, "--trusted-proxy-addr=:8081") || slices.ContainsFunc(container.Args, func(a string) bool { return strings.HasPrefix(a, "--trusted-proxy-cidrs") }) {
		t.Fatalf("controller args = %v", container.Args)
	}
	if !slices.ContainsFunc(container.Ports, func(p corev1.ContainerPort) bool { return p.Name == "proxy" && p.ContainerPort == 8081 }) {
		t.Fatalf("controller ports = %+v", container.Ports)
	}
	var service corev1.Service
	if err := json.Unmarshal(objects["Service"][0], &service); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(service.Spec.Ports, func(p corev1.ServicePort) bool { return p.Name == "proxy" && p.TargetPort.String() == "proxy" }) {
		t.Fatalf("service ports = %+v", service.Spec.Ports)
	}
	var ingress networkingv1.Ingress
	if err := json.Unmarshal(objects["Ingress"][0], &ingress); err != nil {
		t.Fatal(err)
	}
	for _, rule := range ingress.Spec.Rules {
		for _, path := range rule.HTTP.Paths {
			if path.Backend.Service.Port.Name != "proxy" {
				t.Fatalf("ingress reaches the untrusted listener: %+v", path.Backend)
			}
		}
	}
	var fenced bool
	for _, raw := range objects["NetworkPolicy"] {
		var policy networkingv1.NetworkPolicy
		if err := json.Unmarshal(raw, &policy); err != nil {
			t.Fatal(err)
		}
		if policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/component"] != "controller" {
			continue
		}
		fenced = true
		for _, rule := range policy.Spec.Ingress {
			proxy := slices.ContainsFunc(rule.Ports, func(p networkingv1.NetworkPolicyPort) bool { return p.Port != nil && p.Port.String() == "proxy" })
			if !proxy {
				continue
			}
			if len(rule.Ports) != 1 || len(rule.From) != 1 || rule.From[0].NamespaceSelector == nil || rule.From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "ingress-nginx" || rule.From[0].IPBlock != nil {
				t.Fatalf("proxy listener rule = %+v", rule)
			}
		}
	}
	if !fenced {
		t.Fatal("no NetworkPolicy fences the trusted-proxy listener")
	}
	if out := helmRenderError(t, "./sparkwing-full", "sparkwing", "controller.trustedProxyListener.enabled=true"); !strings.Contains(out, "namespaceSelector") {
		t.Fatalf("open trusted listener rendered: %s", out)
	}
}
