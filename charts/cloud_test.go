package charts

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

func TestCloudExampleRendersCompleteInstall(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(helm, "template", "sparkwing", "./sparkwing-full", "--namespace", "sparkwing", "-f", "../examples/cloud.yaml").CombinedOutput()
	if err != nil {
		t.Fatalf("Cloud example: %v\n%s", err, out)
	}
	checkCloudBindings(t, out)
	resources := renderedResources(t, string(out))
	assertValidUniqueResourceNames(t, resources)
	controller := componentResource(t, resources, "Deployment", "controller")
	args := resourceContainer(t, controller).Args
	for _, want := range []string{"--require-auth", "--trusted-proxy-addr=:8081", "--cache-blob-store=s3://sparkwing-cloud-data/controller", "--bucket-store=s3://sparkwing-cloud-data/controller", "--cloudfront-domain=data.sparkwing.dev", "--cloudfront-key-pair-id=REPLACE_PUBLIC_KEY_ID", "--external-url=https://api.sparkwing.dev", "--github-app-id=123456", "--github-app-slug=sparkwing", "--operator-accounts=operator-account-id", "--credentials-dir=/etc/sparkwing/credentials"} {
		if !containsArg(args, want) {
			t.Errorf("controller lacks %q: %v", want, args)
		}
	}
	launcher := componentResource(t, resources, "Deployment", "launcher")
	pod := launcher.Spec.Template.Spec
	if pod.ServiceAccountName != "sparkwing-launcher" || pod.AutomountServiceAccountToken == nil || !*pod.AutomountServiceAccountToken {
		t.Fatalf("launcher Kubernetes identity missing: %+v", pod)
	}
	c := resourceContainer(t, launcher)
	if len(c.Args) == 0 || c.Args[0] != "launch" {
		t.Fatalf("launcher command = %v %v", c.Command, c.Args)
	}
	for _, prefix := range []string{"--image=", "--docker-image="} {
		arg, ok := hasFlag(c.Args, prefix)
		if !ok || !strings.Contains(arg, "@sha256:") {
			t.Errorf("launcher lacks pinned %s: %v", prefix, c.Args)
		}
	}
	for _, want := range []string{"--namespace=sparkwing-cloud-jobs", "--controller=https://api.sparkwing.dev", "--credentials-dir=/etc/sparkwing/credentials", "--cpu-ceiling=2", "--memory-ceiling=8Gi"} {
		if !containsArg(c.Args, want) {
			t.Errorf("launcher lacks %q", want)
		}
	}
	kinds := map[string]bool{}
	for _, r := range resources {
		kinds[r.Kind] = true
		if r.Kind == "Deployment" && r.Metadata.Labels["app.kubernetes.io/component"] == "runner" {
			t.Error("Cloud example starts a pool runner")
		}
		for _, removed := range []string{"cache", "gitcache", "logs", "web"} {
			if r.Metadata.Labels["app.kubernetes.io/component"] == removed {
				t.Errorf("removed component: %s", removed)
			}
		}
	}
	for _, kind := range []string{"Service", "Ingress", "Namespace", "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding", "NetworkPolicy", "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
		if !kinds[kind] {
			t.Errorf("missing %s", kind)
		}
	}
	for _, resource := range resources {
		if resource.Kind == "PersistentVolumeClaim" && !strings.HasSuffix(resource.Metadata.Name, "-credential-epoch") {
			t.Errorf("RDS/S3 install has an unexpected PVC %s", resource.Metadata.Name)
		}
	}
}

func TestImagesRequireRepositoryAndTagTogether(t *testing.T) {
	for _, chart := range []string{"./sparkwing-full", "./sparkwing-runner-bundle"} {
		prefix := "runner"
		if strings.HasSuffix(chart, "sparkwing-full") {
			prefix = "controller"
		}
		for _, field := range []string{"repository", "tag"} {
			out := helmRenderError(t, chart, "sparkwing", prefix+".image."+field+"=")
			if !strings.Contains(out, "image."+field+" is required") {
				t.Errorf("missing %s error: %s", field, out)
			}
		}
	}
}

func checkCloudBindings(t *testing.T, manifest []byte) {
	t.Helper()
	decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(manifest)), 4096)
	ingressFound, controllerFound, launcherFound := false, false, false
	var policies []networkingv1.NetworkPolicy
	jobNamespace := ""
	for {
		var raw runtime.RawExtension
		err := decoder.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var meta struct{ Kind string }
		if err := json.Unmarshal(raw.Raw, &meta); err != nil {
			t.Fatal(err)
		}
		switch meta.Kind {
		case "Deployment":
			var d appsv1.Deployment
			if err := json.Unmarshal(raw.Raw, &d); err != nil {
				t.Fatal(err)
			}
			component := d.Labels["app.kubernetes.io/component"]
			if component != "controller" && component != "launcher" {
				continue
			}
			if d.Spec.Template.Labels["app"] != "sparkwing-"+component {
				t.Errorf("%s is outside baseline policy selectors", component)
			}
			pod := d.Spec.Template.Spec
			if component == "controller" {
				if pod.SecurityContext == nil || pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 65534 || pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != 65534 {
					t.Fatal("controller data identity is missing")
				}
				owned := false
				for _, init := range pod.InitContainers {
					if slices.Equal(init.Command, []string{"/bin/chown"}) && slices.Equal(init.Args, []string{"65534:65534", "/data", "/var/lib/sparkwing/credential-epoch"}) && init.SecurityContext != nil && init.SecurityContext.RunAsUser != nil && *init.SecurityContext.RunAsUser == 0 {
						for _, mount := range init.VolumeMounts {
							owned = owned || mount.Name == "data" && mount.MountPath == "/data" && !mount.ReadOnly
						}
					}
				}
				if !owned {
					t.Error("controller cannot chmod the root-owned data volume to 0700")
				}
			}
			paths := map[string]string{}
			for _, volume := range pod.Volumes {
				if volume.Projected == nil {
					continue
				}
				if volume.Projected.DefaultMode == nil || *volume.Projected.DefaultMode != 0o400 {
					t.Errorf("%s credential mode is not 0400", component)
				}
				for _, source := range volume.Projected.Sources {
					if source.Secret == nil {
						continue
					}
					if len(source.Secret.Items) == 0 {
						paths[source.Secret.Name] = "bundle"
					}
					for _, item := range source.Secret.Items {
						paths[item.Path] = source.Secret.Name + "/" + item.Key
					}
				}
			}
			for _, container := range pod.Containers {
				for _, env := range container.Env {
					if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
						t.Errorf("%s puts credentials in environment", component)
					}
				}
			}
			if component == "controller" {
				controllerFound = true
				for path, want := range map[string]string{"pg-url": "sparkwing-database/dsn", "bootstrap-admin-token": "sparkwing-bootstrap-admin/token", "secrets-key": "sparkwing-secrets-key/key", "sparkwing-credentials": "bundle"} {
					if paths[path] != want {
						t.Errorf("credential %s = %s, want %s", path, paths[path], want)
					}
				}
				mounted := false
				for _, mount := range pod.Containers[0].VolumeMounts {
					if mount.Name == "data" && mount.MountPath == "/data" {
						mounted = true
					}
				}
				if !mounted {
					t.Error("controller home not mounted at /data")
				}
				for _, volume := range pod.Volumes {
					if volume.Name == "data" && volume.EmptyDir == nil {
						t.Error("RDS/S3 home must be ephemeral")
					}
				}
			} else {
				launcherFound = true
				for _, arg := range pod.Containers[0].Args {
					if ns, ok := strings.CutPrefix(arg, "--namespace="); ok {
						jobNamespace = ns
					}
				}
				if paths["agent-token"] != "sparkwing-launcher-token/token" {
					t.Errorf("launcher credential = %v", paths)
				}
			}
		case "NetworkPolicy":
			var policy networkingv1.NetworkPolicy
			if err := json.Unmarshal(raw.Raw, &policy); err != nil {
				t.Fatal(err)
			}
			policies = append(policies, policy)
		case "Ingress":
			ingressFound = true
			var ingress networkingv1.Ingress
			if err := json.Unmarshal(raw.Raw, &ingress); err != nil {
				t.Fatal(err)
			}
			if ingress.Spec.IngressClassName == nil || *ingress.Spec.IngressClassName != "nginx" {
				t.Error("Cloud ingress class is not nginx")
			}
			hosts := map[string]bool{}
			for _, rule := range ingress.Spec.Rules {
				hosts[rule.Host] = true
				if rule.HTTP == nil {
					t.Fatal("ingress lacks HTTP paths")
				}
				for _, path := range rule.HTTP.Paths {
					if path.Backend.Service == nil || !strings.HasSuffix(path.Backend.Service.Name, "-controller") {
						t.Errorf("ingress path misses controller: %+v", path)
					}
				}
			}
			if len(hosts) != 2 || !hosts["api.sparkwing.dev"] || !hosts["console.sparkwing.dev"] || len(ingress.Spec.TLS) != 1 || ingress.Spec.TLS[0].SecretName != "sparkwing-tls" {
				t.Errorf("Cloud ingress hosts/TLS = %+v", ingress.Spec)
			}
		}
	}
	if jobNamespace == "" {
		t.Error("launcher names no Job namespace")
	}
	for _, problem := range jobPolicyProblems(policies, jobNamespace) {
		t.Error(problem)
	}
	if !ingressFound || !controllerFound || !launcherFound {
		t.Fatal("Cloud bindings were not exercised")
	}
}

func TestCloudLauncherRequiresDigestAndSeparateCredential(t *testing.T) {
	for _, test := range []struct{ set, want string }{
		{"sparkwing-runner-bundle.launcher.dockerImage.digest=", "digest is required"},
		{"sparkwing-runner-bundle.launcher.jobImage.digest=latest", "digest must be sha256"},
		{"sparkwing-runner-bundle.launcher.tokenSecret.name=", "launcher.tokenSecret.name is required"},
		{"sparkwing-runner-bundle.launcher.image.tag=", "image.tag is required"},
	} {
		t.Run(test.set, func(t *testing.T) {
			helm, err := exec.LookPath("helm")
			if err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(helm, "template", "sparkwing", "./sparkwing-full", "-f", "../examples/cloud.yaml", "--set", test.set).CombinedOutput()
			if err == nil || !strings.Contains(string(out), test.want) {
				t.Fatalf("render error = %v %s, want %s", err, out, test.want)
			}
		})
	}
}

// safety: the Job's real labels come from the launcher; the team and job values vary per Job, so a selector
// constraining only those keys can still select some Job and is judged.
func selectsSomeCloudJob(selector metav1.LabelSelector, namespace string) (bool, error) {
	parsed, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		return false, err
	}
	fixed := launcher.JobPodLabels(namespace, "team", "job")
	requirements, _ := parsed.Requirements()
	for _, requirement := range requirements {
		if requirement.Key() == launcher.JobLabel || requirement.Key() == launcher.TeamLabel {
			if requirement.Operator() == selection.DoesNotExist {
				return false, nil
			}
			continue
		}
		if !requirement.Matches(labels.Set(fixed)) {
			return false, nil
		}
	}
	return true, nil
}

// safety: a Pod's traffic is the union of every policy selecting it in its namespace, so all are judged together:
// no ingress, and egress only to cluster DNS and the internet blocks with private ranges excepted.
func jobPolicyProblems(policies []networkingv1.NetworkPolicy, namespace string) []string {
	var problems []string
	isolatedIn, isolatedOut, internet := false, false, false
	want := map[string][]string{"0.0.0.0/0": {"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10"}, "::/0": {"fc00::/7", "fe80::/10"}}
	for _, policy := range policies {
		selects, err := selectsSomeCloudJob(policy.Spec.PodSelector, namespace)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", policy.Name, err))
			continue
		}
		if policy.Namespace != namespace || !selects {
			continue
		}
		isolatedIn = isolatedIn || slices.Contains(policy.Spec.PolicyTypes, networkingv1.PolicyTypeIngress)
		isolatedOut = isolatedOut || slices.Contains(policy.Spec.PolicyTypes, networkingv1.PolicyTypeEgress)
		if len(policy.Spec.Ingress) != 0 {
			problems = append(problems, fmt.Sprintf("%s allows ingress to Cloud Jobs: %+v", policy.Name, policy.Spec.Ingress))
		}
		for _, rule := range policy.Spec.Egress {
			if len(rule.To) == 0 {
				problems = append(problems, fmt.Sprintf("%s allows Cloud Jobs egress to every destination", policy.Name))
			}
			for _, peer := range rule.To {
				if clusterDNSPeer(peer) {
					for _, port := range rule.Ports {
						if port.Port == nil || port.Port.IntValue() != 53 || port.EndPort != nil {
							problems = append(problems, fmt.Sprintf("%s allows Cloud Jobs cluster DNS traffic on port %+v", policy.Name, port))
						}
					}
					if len(rule.Ports) == 0 {
						problems = append(problems, fmt.Sprintf("%s allows Cloud Jobs every port on cluster DNS", policy.Name))
					}
					continue
				}
				if peer.IPBlock == nil || peer.PodSelector != nil || peer.NamespaceSelector != nil {
					problems = append(problems, fmt.Sprintf("%s allows Cloud Jobs egress to in-cluster peer %+v", policy.Name, peer))
					continue
				}
				if len(rule.Ports) != 0 {
					problems = append(problems, fmt.Sprintf("%s limits Cloud internet egress to ports %+v", policy.Name, rule.Ports))
				}
				if except, ok := want[peer.IPBlock.CIDR]; !ok || !slices.Equal(peer.IPBlock.Except, except) {
					problems = append(problems, fmt.Sprintf("%s: Cloud egress block %s excepts %v, want %v", policy.Name, peer.IPBlock.CIDR, peer.IPBlock.Except, except))
				}
				internet = true
			}
		}
	}
	if !isolatedIn || !isolatedOut {
		problems = append(problems, fmt.Sprintf("no NetworkPolicy in %s isolates Cloud Job ingress and egress", namespace))
	}
	if !internet {
		problems = append(problems, "Cloud Job policy allows no internet egress")
	}
	return problems
}

func clusterDNSPeer(peer networkingv1.NetworkPolicyPeer) bool {
	return peer.IPBlock == nil && peer.NamespaceSelector != nil && peer.PodSelector != nil &&
		len(peer.NamespaceSelector.MatchExpressions) == 0 && len(peer.PodSelector.MatchExpressions) == 0 &&
		maps.Equal(peer.NamespaceSelector.MatchLabels, map[string]string{"kubernetes.io/metadata.name": "kube-system"}) &&
		maps.Equal(peer.PodSelector.MatchLabels, map[string]string{"k8s-app": "kube-dns"})
}

func TestJobPolicyEvaluationRefusesAdditivePermissions(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(helm, "template", "sparkwing", "./sparkwing-full", "--namespace", "sparkwing", "-f", "../examples/cloud.yaml").CombinedOutput()
	if err != nil {
		t.Fatalf("Cloud example: %v\n%s", err, out)
	}
	var rendered []networkingv1.NetworkPolicy
	decoder := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(out)), 4096)
	for {
		var policy networkingv1.NetworkPolicy
		err := decoder.Decode(&policy)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if policy.Kind == "NetworkPolicy" {
			rendered = append(rendered, policy)
		}
	}
	namespace := "sparkwing-cloud-jobs"
	if problems := jobPolicyProblems(rendered, namespace); len(problems) != 0 {
		t.Fatalf("rendered policies: %v", problems)
	}
	for name, mutate := range map[string]func([]networkingv1.NetworkPolicy) []networkingv1.NetworkPolicy{
		"another policy allows all egress": func(p []networkingv1.NetworkPolicy) []networkingv1.NetworkPolicy {
			return append(p, networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "extra", Namespace: namespace}, Spec: networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{{}}}})
		},
		"another policy reaches the cluster": func(p []networkingv1.NetworkPolicy) []networkingv1.NetworkPolicy {
			return append(p, networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "extra", Namespace: namespace}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "sparkwing-job"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{}}}}}}})
		},
		"another policy selects by team label": func(p []networkingv1.NetworkPolicy) []networkingv1.NetworkPolicy {
			return append(p, networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "extra", Namespace: namespace}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: launcher.TeamLabel, Operator: metav1.LabelSelectorOpExists}}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{{}}}})
		},
		"another policy selects one team": func(p []networkingv1.NetworkPolicy) []networkingv1.NetworkPolicy {
			return append(p, networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "extra", Namespace: namespace}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{launcher.TeamLabel: "some-team-hash"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}, Egress: []networkingv1.NetworkPolicyEgressRule{{}}}})
		},
		"deny selects other pods": func(p []networkingv1.NetworkPolicy) []networkingv1.NetworkPolicy {
			out := slices.Clone(p)
			for i := range out {
				out[i].Spec.PodSelector = metav1.LabelSelector{MatchLabels: map[string]string{"app": "other"}}
			}
			return out
		},
		"policies sit in another namespace": func(p []networkingv1.NetworkPolicy) []networkingv1.NetworkPolicy {
			out := slices.Clone(p)
			for i := range out {
				out[i].Namespace = "elsewhere"
			}
			return out
		},
	} {
		if problems := jobPolicyProblems(mutate(slices.Clone(rendered)), namespace); len(problems) == 0 {
			t.Errorf("%s: evaluation accepted the policy set", name)
		}
	}
}
