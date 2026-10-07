package charts

import (
	"strings"
	"testing"
)

func secretRef(t *testing.T, c renderedContainer, name string) *renderedSecretKeyRef {
	t.Helper()
	for _, env := range c.Env {
		if env.Name == name && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			return env.ValueFrom.SecretKeyRef
		}
	}
	return nil
}

func sameSecret(a, b *renderedSecretKeyRef) bool {
	return a != nil && b != nil && a.Name == b.Name && a.Key == b.Key
}

func TestCacheReadsItsSecretsFromTheCredentialsDir(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	rendered := renderCache(t)
	container := runnerContainer(t, rendered)
	if dir, _ := flagValue(container.Args, "--credentials-dir"); dir != "/etc/sparkwing/credentials" {
		t.Fatalf("cache args = %v, want --credentials-dir /etc/sparkwing/credentials", container.Args)
	}
	mounted := false
	for _, mount := range container.VolumeMounts {
		mounted = mounted || (mount.Name == "credentials" && mount.MountPath == "/etc/sparkwing/credentials")
	}
	if !mounted {
		t.Errorf("cache mounts = %+v, want the credentials volume at its --credentials-dir", container.VolumeMounts)
	}
	for _, env := range container.Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			t.Errorf("cache env %s carries a Secret; its secrets belong in the credentials directory", env.Name)
		}
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

// Team code runs beside the runner's token, so neither the cache's operator
// token nor the key grants are signed with may be that token: either would
// let one team's pipeline mint a grant for any team, or act as the operator.
func TestCacheSecretsAreNeverTheRunnersToken(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1s of real work; the fast class runs under -short")
	}
	runner := renderRunner(t)
	cache := renderCache(t)
	controller := renderController(t)
	agent := credentialRef(t, runner, "agent-token")
	if agent == nil {
		t.Fatal("the runner projects no agent-token credential")
	}
	for name, ref := range map[string]*renderedSecretKeyRef{
		"cache cache-token":                    credentialRef(t, cache, "cache-token"),
		"cache cache-grant-key":                credentialRef(t, cache, "cache-grant-key"),
		"controller SPARKWING_CACHE_TOKEN":     secretRef(t, controller, "SPARKWING_CACHE_TOKEN"),
		"controller SPARKWING_CACHE_GRANT_KEY": secretRef(t, controller, "SPARKWING_CACHE_GRANT_KEY"),
	} {
		if ref == nil {
			t.Errorf("%s is not a secretKeyRef", name)
			continue
		}
		if sameSecret(ref, agent) {
			t.Errorf("%s reads %s/%s, the runner's own token", name, ref.Name, ref.Key)
		}
	}
	if !sameSecret(credentialRef(t, cache, "cache-token"), secretRef(t, controller, "SPARKWING_CACHE_TOKEN")) {
		t.Error("the controller's cache token is not the cache's operator token")
	}
	if !sameSecret(credentialRef(t, cache, "cache-grant-key"), secretRef(t, controller, "SPARKWING_CACHE_GRANT_KEY")) {
		t.Error("the controller signs grants with a key the cache does not verify with")
	}
	if ref := credentialRef(t, runner, "cache-grant-key"); ref != nil {
		t.Errorf("the runner carries the grant key %s/%s", ref.Name, ref.Key)
	}
}

// Pointing two of the three secrets at one Secret key fails the render
// rather than handing team code the cache.
func TestSharedCacheSecretsFailAtRender(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1s of real work; the fast class runs under -short")
	}
	for name, sets := range map[string][]string{
		"operator token is the runner token": {
			"cache.tokenSecret.name=sparkwing-token", "cache.tokenSecret.key=token",
		},
		"grant key is the runner token": {
			"cache.grantKeySecret.name=sparkwing-token", "cache.grantKeySecret.key=token",
		},
		"grant key is the operator token": {
			"cache.grantKeySecret.name=sparkwing-cache-token", "cache.grantKeySecret.key=token",
		},
	} {
		out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing", sets...)
		if !strings.Contains(out, "must name a different Secret key") {
			t.Errorf("%s: render error does not explain the separation:\n%s", name, out)
		}
	}
}
