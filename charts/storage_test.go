package charts

import (
	"io"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestChartsUseControllerDataWithoutCacheService(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: renders both Helm charts")
	}
	for _, chart := range []string{"./sparkwing-runner-bundle", "./sparkwing-full"} {
		t.Run(chart, func(t *testing.T) {
			resources := renderedResources(t, helmRenderAll(t, chart, "sparkwing", "default", "controller.url=https://controller.example.com"))
			for _, resource := range resources {
				if resource.Metadata.Labels["app.kubernetes.io/component"] == "cache" {
					t.Fatalf("cache resource remains: %s %s", resource.Kind, resource.Metadata.Name)
				}
				if resource.Kind != "Deployment" {
					continue
				}
				for _, container := range resource.Spec.Template.Spec.Containers {
					for _, arg := range container.Args {
						for _, prefix := range []string{"--cache-url=", "--gitcache=", "--dependency-proxy=", "--trigger-artifact-store=http"} {
							if strings.HasPrefix(arg, prefix) {
								t.Errorf("cache-service argument remains: %s", arg)
							}
						}
					}
					for _, env := range container.Env {
						if env.Name == "SPARKWING_GITCACHE_URL" || env.Name == "SPARKWING_CACHE_URL" || strings.Contains(env.Value, "/proxy/") {
							t.Errorf("cache-service environment remains: %s", env.Name)
						}
					}
				}
			}
		})
	}
}

func TestControllerEpochSurvivesPodAndDatabaseReplacement(t *testing.T) {
	for _, storage := range []string{"pvc", "emptyDir"} {
		t.Run(storage, func(t *testing.T) {
			sets := []string{"controller.store=postgres", "controller.databaseSecret.name=database", "controller.dataStore.s3URL=s3://bucket/controller", "controller.storage.type=" + storage}
			rendered := helmRenderAll(t, "./sparkwing-full", "sparkwing", "default", sets...)
			pod := deploymentDocument(t, helmRender(t, "./sparkwing-full", "templates/controller-deployment.yaml", "sparkwing", sets...)).Spec.Template.Spec
			controller := pod.Containers[0]
			if !containsArg(controller.Args, "--credential-epoch-dir=/var/lib/sparkwing/credential-epoch") {
				t.Fatalf("controller does not select the persistent epoch directory: %v", controller.Args)
			}
			if !hasMount(controller.VolumeMounts, "credential-epoch", "/var/lib/sparkwing/credential-epoch") {
				t.Fatalf("controller lacks the separate epoch volume: %+v", controller.VolumeMounts)
			}
			for _, mount := range controller.VolumeMounts {
				if mount.Name == "credential-epoch" && mount.ReadOnly {
					t.Fatal("credential epoch volume is read-only")
				}
				if mount.Name == "credentials" && !mount.ReadOnly {
					t.Fatal("provider credentials must stay read-only")
				}
			}
			claim := claimVolume(pod.Volumes, "credential-epoch")
			if claim == "" || claim == claimVolume(pod.Volumes, "data") {
				t.Fatalf("epoch needs a dedicated persistent claim: %+v", pod.Volumes)
			}
			found := false
			dec := yaml.NewDecoder(strings.NewReader(rendered))
			for {
				var resource renderedClaim
				if err := dec.Decode(&resource); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if resource.Kind == "PersistentVolumeClaim" && resource.Metadata.Name == claim {
					found = true
					if resource.Metadata.Annotations["helm.sh/resource-policy"] != "keep" {
						t.Fatal("epoch claim must survive chart uninstall")
					}
				}
			}
			if !found {
				t.Fatalf("epoch claim %s was not rendered", claim)
			}
		})
	}
}

func TestRunnerReadsItsTokenFromTheCredentialsDir(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	container := runnerContainer(t, renderRunner(t))
	if !containsArg(container.Args, "--credentials-dir=/etc/sparkwing/credentials") {
		t.Fatalf("runner args = %v, want --credentials-dir=/etc/sparkwing/credentials", container.Args)
	}
	for _, env := range container.Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			t.Errorf("runner env %s carries a Secret; its token belongs in the credentials directory", env.Name)
		}
	}
}

// safety: an emptyDir /data goes with the pod, so the chart refuses it
// whenever state.db or the blob store would live there. A bucket-ceiling
// URL only measures a bucket and does not move blobs off /data.
func TestEphemeralDataNeedsDurableStateAndBlobs(t *testing.T) {
	postgres := []string{"controller.store=postgres", "controller.databaseSecret.name=database", "controller.storage.type=emptyDir"}
	for name, tc := range map[string]struct {
		sets []string
		want string
	}{
		"sqlite":                  {[]string{"controller.storage.type=emptyDir"}, "state.db"},
		"filesystem blobs":        {postgres, "controller.dataStore.s3URL"},
		"bucket measurement only": {append(postgres, "controller.bucketCeiling.store=s3://bucket/controller"), "controller.dataStore.s3URL"},
	} {
		t.Run(name, func(t *testing.T) {
			if out := helmRenderError(t, "./sparkwing-full", "sparkwing", tc.sets...); !strings.Contains(out, tc.want) {
				t.Fatalf("render failure does not name %q:\n%s", tc.want, out)
			}
		})
	}
	helmRenderAll(t, "./sparkwing-full", "sparkwing", "default", append(postgres, "controller.dataStore.s3URL=s3://bucket/controller")...)
}
