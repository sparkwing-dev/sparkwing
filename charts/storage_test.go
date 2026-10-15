package charts

import (
	"strings"
	"testing"
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
