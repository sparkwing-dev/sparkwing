package charts

import (
	"slices"
	"strings"
	"testing"
)

func TestFullChartDefaultsDeclareALocalSQLiteControllerThatRequiresAuth(t *testing.T) {
	args := renderController(t).Args
	for _, want := range []string{"--deployment=local", "--store=sqlite", "--require-auth"} {
		if !containsArg(args, want) {
			t.Errorf("default controller args = %v, want %q", args, want)
		}
	}
}

func TestFullChartRendersTheDeclaredDeploymentAndStore(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: helm render")
	}
	for _, test := range []struct {
		sets []string
		want []string
	}{
		{[]string{"controller.deployment=team"}, []string{"--deployment=team", "--store=sqlite"}},
		{[]string{"controller.deployment=cloud", "controller.store=postgres", "controller.databaseSecret.name=sparkwing-db"}, []string{"--deployment=cloud", "--store=postgres"}},
		// safety: helm cannot see a credentials bundle's keys, so pg-url may arrive there.
		{[]string{"controller.store=postgres", "controller.credentialsSecret.name=sparkwing-credentials"}, []string{"--deployment=local", "--store=postgres"}},
	} {
		args := renderController(t, test.sets...).Args
		for _, want := range test.want {
			if !containsArg(args, want) {
				t.Errorf("%v: controller args = %v, want %q", test.sets, args, want)
			}
		}
	}
}

// safety: a mode value outside its choices used to fall through to the
// template's default branch, so storage.type=PVC lost /data with the pod.
func TestFullChartSchemaRefusesModeValueTypos(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: helm render")
	}
	for set, field := range map[string]string{
		"controller.storage.type=PVC":                "/controller/storage/type",
		"controller.deployment=teams":                "/controller/deployment",
		"controller.store=postgresql":                "/controller/store",
		"controller.limitsProfile=cloudfree":         "/controller/limitsProfile",
		"controller.image.pullPolicy=always":         "/controller/image/pullPolicy",
		"volumePermissions.enabled=yes":              "/volumePermissions/enabled",
		"controller.trustedProxyListener.enabled=on": "/controller/trustedProxyListener/enabled",
	} {
		t.Run(set, func(t *testing.T) {
			out := helmRenderError(t, "./sparkwing-full", "sparkwing", set)
			if !strings.Contains(out, field) {
				t.Fatalf("render error does not name %s:\n%s", field, out)
			}
		})
	}
}

func TestFullChartRefusesAStoreThatDisagreesWithItsCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: helm render")
	}
	for _, test := range []struct {
		sets []string
		want string
	}{
		{[]string{"controller.databaseSecret.name=sparkwing-db"}, "controller.databaseSecret.name needs controller.store=postgres"},
		{[]string{"controller.store=postgres"}, "controller.store=postgres needs a pg-url credential"},
		{[]string{"controller.deployment=cloud"}, "controller.deployment=cloud needs controller.store=postgres"},
		{[]string{"controller.deployment=cloud", "controller.databaseSecret.name=sparkwing-db"}, "controller.databaseSecret.name needs controller.store=postgres"},
	} {
		t.Run(strings.Join(test.sets, ","), func(t *testing.T) {
			out := helmRenderError(t, "./sparkwing-full", "sparkwing", test.sets...)
			if !strings.Contains(out, test.want) {
				t.Fatalf("render error lacks %q:\n%s", test.want, out)
			}
		})
	}
}

// safety: the old defaults were requireAuth=true and allowOpenBootstrap=false,
// so a release carrying them under --reuse-values must hear about the removal
// rather than keep an opt-out it no longer has.
func TestRemovedAuthValuesFailTheRender(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: helm render")
	}
	for _, set := range []string{
		"controller.requireAuth=true",
		"controller.requireAuth=false",
		"controller.allowOpenBootstrap=false",
		"controller.allowOpenBootstrap=true",
	} {
		t.Run(set, func(t *testing.T) {
			value, _, _ := strings.Cut(set, "=")
			out := helmRenderError(t, "./sparkwing-full", "sparkwing", set)
			for _, want := range []string{value + " was removed", "always passes --require-auth", value + "=null"} {
				if !strings.Contains(out, want) {
					t.Fatalf("render error lacks %q:\n%s", want, out)
				}
			}
		})
	}
	renderController(t, "controller.requireAuth=null", "controller.allowOpenBootstrap=null")
}

// safety: removing the bootstrap seed after handoff used to force dropping
// --require-auth, which left the controller without its startup credential check.
func TestFullChartAlwaysRendersRequireAuth(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: helm render")
	}
	for _, sets := range [][]string{
		nil,
		{"controller.bootstrapAdminToken.name=sparkwing-bootstrap-admin"},
		{"controller.credentialsSecret.name=sparkwing-credentials"},
		{"ingress.enabled=true", "ingress.tls[0].secretName=sparkwing-tls"},
		{"ingress.enabled=true", "ingress.allowInsecure=true"},
	} {
		if args := renderController(t, sets...).Args; !slices.Contains(args, "--require-auth") {
			t.Errorf("%v: controller args = %v, want --require-auth", sets, args)
		}
	}
}
