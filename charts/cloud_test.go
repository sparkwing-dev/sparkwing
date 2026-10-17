package charts

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

func TestCloudExampleRendersCompleteInstall(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(helm, "template", "sparkwing", "./sparkwing-full", "--namespace", "sparkwing", "-f", "sparkwing-full/examples/cloud.yaml").CombinedOutput()
	if err != nil {
		t.Fatalf("Cloud example: %v\n%s", err, out)
	}
	checkCloudBindings(t, out)
	resources := renderedResources(t, string(out))
	assertValidUniqueResourceNames(t, resources)
	controller := componentResource(t, resources, "Deployment", "controller")
	args := resourceContainer(t, controller).Args
	for _, want := range []string{"--require-auth", "--cache-blob-store=s3://sparkwing-cloud-data", "--cloudfront-domain=data.sparkwing.dev", "--cloudfront-key-pair-id=REPLACE_PUBLIC_KEY_ID", "--external-url=https://api.sparkwing.dev", "--github-app-id=123456", "--github-app-slug=sparkwing", "--operator-accounts=operator-account-id", "--credentials-dir=/etc/sparkwing/credentials"} {
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
	for _, want := range []string{"--namespace=sparkwing", "--controller=https://api.sparkwing.dev", "--credentials-dir=/etc/sparkwing/credentials", "--cpu-ceiling=2", "--memory-ceiling=8Gi"} {
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
	for _, kind := range []string{"Service", "Ingress", "ServiceAccount", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding", "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "NetworkPolicy"} {
		if !kinds[kind] {
			t.Errorf("missing %s", kind)
		}
	}
	if kinds["PersistentVolumeClaim"] {
		t.Error("RDS/S3 install requires a PVC")
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
			pod := d.Spec.Template.Spec
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
				if paths["agent-token"] != "sparkwing-launcher-token/token" {
					t.Errorf("launcher credential = %v", paths)
				}
			}
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
			out, err := exec.Command(helm, "template", "sparkwing", "./sparkwing-full", "-f", "sparkwing-full/examples/cloud.yaml", "--set", test.set).CombinedOutput()
			if err == nil || !strings.Contains(string(out), test.want) {
				t.Fatalf("render error = %v %s, want %s", err, out, test.want)
			}
		})
	}
}
