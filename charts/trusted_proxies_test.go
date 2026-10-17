package charts

import "testing"

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
