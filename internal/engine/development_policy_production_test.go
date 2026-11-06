//go:build !qatrust

package engine

import "testing"

func TestRequirePolicyBuildRefusesMalformedPseudoVersions(t *testing.T) {
	for _, version := range []string{
		"v0.68.7-0.20261306041046-718a7591f23a",
		"v0.68.7-0.20260230041046-718a7591f23a",
		"v0.68.7-0.2026110604104-718a7591f23a",
		"v0.68.7-0.20261106041046-718A7591F23A",
		"v0.68.7-0.20261106041046-718a7591f23",
		"v0.68.7-0.20261106041046-718a7591f23aa",
		"v0.68.7-0.20261106041046-718a7591f23g",
		"v0.68.7-0.20261106041046-718a7591f23a+dirty",
		"v0.68.7-0.20261106041046-718a7591f23a+incompatible",
		"v0.0.0-20261106041046-718a7591f23a",
		"v0.68.0-0.20261106041046-718a7591f23a",
		"v0.68.7-rc.1.0.20261106041046-718a7591f23a",
		"v0.068.7-0.20261106041046-718a7591f23a",
	} {
		if err := RequirePolicyBuild("Cloud job", version, "", false); err == nil {
			t.Errorf("accepted malformed main build %q", version)
		}
	}
}
