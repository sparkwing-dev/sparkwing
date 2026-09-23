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

// Team code runs beside the runner's token, so neither the cache's operator
// token nor the key grants are signed with may be that token: either would
// let one team's pipeline mint a grant for any team, or act as the operator.
func TestCacheSecretsAreNeverTheRunnersToken(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1s of real work; the fast class runs under -short")
	}
	runner := runnerContainer(t, renderRunner(t))
	cache := runnerContainer(t, renderCache(t))
	controller := renderController(t)
	agent := secretRef(t, runner, "SPARKWING_AGENT_TOKEN")
	if agent == nil {
		t.Fatal("the runner carries no SPARKWING_AGENT_TOKEN secretKeyRef")
	}
	for name, ref := range map[string]*renderedSecretKeyRef{
		"cache SPARKWING_API_TOKEN":            secretRef(t, cache, "SPARKWING_API_TOKEN"),
		"cache SPARKWING_CACHE_GRANT_KEY":      secretRef(t, cache, "SPARKWING_CACHE_GRANT_KEY"),
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
	if !sameSecret(secretRef(t, cache, "SPARKWING_API_TOKEN"), secretRef(t, controller, "SPARKWING_CACHE_TOKEN")) {
		t.Error("the controller's cache token is not the cache's operator token")
	}
	if !sameSecret(secretRef(t, cache, "SPARKWING_CACHE_GRANT_KEY"), secretRef(t, controller, "SPARKWING_CACHE_GRANT_KEY")) {
		t.Error("the controller signs grants with a key the cache does not verify with")
	}
	if ref := secretRef(t, runner, "SPARKWING_CACHE_GRANT_KEY"); ref != nil {
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
			"cache.tokenSecret.name=sparkwing-token", "cache.tokenSecret.key=token"},
		"grant key is the runner token": {
			"cache.grantKeySecret.name=sparkwing-token", "cache.grantKeySecret.key=token"},
		"grant key is the operator token": {
			"cache.grantKeySecret.name=sparkwing-cache-token", "cache.grantKeySecret.key=token"},
	} {
		out := helmRenderError(t, "./sparkwing-runner-bundle", "sparkwing", sets...)
		if !strings.Contains(out, "must name a different Secret key") {
			t.Errorf("%s: render error does not explain the separation:\n%s", name, out)
		}
	}
}
