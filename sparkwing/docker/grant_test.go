package docker

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/planguard"
)

func grantedCtx(ctx context.Context) context.Context {
	return planguard.Grant(ctx)
}

func refusalFrom(call func()) (msg string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
			msg = fmt.Sprint(r)
		}
	}()
	call()
	return "", false
}

func TestEveryGuardedHelperRefusesAContextCarryingNoGrant(t *testing.T) {
	bg := context.Background()
	for _, tc := range []struct {
		helper string
		call   func()
	}{
		{"docker.ComputeTags", func() { _, _ = ComputeTags(bg) }},
		{"docker.ComputeTagsIn", func() { _, _ = ComputeTagsIn(bg, ".") }},
		{"docker.Build", func() { _, _ = Build(bg, BuildConfig{}) }},
		{"docker.BuildAndPush", func() { _, _ = BuildAndPush(bg, BuildConfig{}) }},
		{"docker.Push", func() { _ = Push(bg, "example/image", nil, nil) }},
		{"docker.Login", func() { _ = Login(bg, "registry.example", "user", "token") }},
		{"docker.BuildxPlatforms", func() { _, _ = BuildxPlatforms(bg) }},
		{"docker.FilterBuildxPlatforms", func() { _, _ = FilterBuildxPlatforms(bg, nil) }},
		{"docker.Run", func() { _ = Run(bg, RunOptions{}) }},
	} {
		t.Run(tc.helper, func(t *testing.T) {
			msg, panicked := refusalFrom(tc.call)
			if !panicked {
				t.Fatalf("%s ran on a context carrying no grant", tc.helper)
			}
			if !strings.Contains(msg, tc.helper+" called") {
				t.Errorf("refusal = %q, want it to name %s so the author finds the call", msg, tc.helper)
			}
		})
	}
}
