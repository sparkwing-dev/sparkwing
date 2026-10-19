//go:build qatrust

package sdkinstall

import (
	"slices"
	"testing"
)

func TestQATrustBuildPutsTheQAProxyAheadOfTheCallers(t *testing.T) {
	for _, tc := range []struct {
		environment, want []string
	}{
		{nil, []string{"GOPROXY=https://qa-releases.sparkwing.dev/goproxy|https://proxy.golang.org,direct", "GONOSUMDB=github.com/sparkwing-dev/sparkwing"}},
		{[]string{"GOPROXY=file:///proxy", "GONOSUMDB=example.com/private"}, []string{"GOPROXY=https://qa-releases.sparkwing.dev/goproxy|file:///proxy", "GONOSUMDB=example.com/private,github.com/sparkwing-dev/sparkwing"}},
	} {
		if got := qaModuleEnvironment(tc.environment); !slices.Equal(got, tc.want) {
			t.Errorf("qaModuleEnvironment(%v) = %v, want %v", tc.environment, got, tc.want)
		}
	}
}
