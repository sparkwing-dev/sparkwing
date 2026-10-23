package nodehost

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/pkg/nodeproto"
)

func FuzzReplacementHelloPreservesProtocolAndLanguage(f *testing.F) {
	f.Add(nodeproto.Version, "go", "(devel)", true)
	f.Add(nodeproto.Version, "go", "v1.0.0", false)
	f.Add(nodeproto.Version+1, "typescript", "anything", true)
	f.Fuzz(func(t *testing.T, protocol int, language, version string, replacement bool) {
		pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v1.0.0"}
		if replacement {
			pin.Replacement = "../sdk"
		}
		process := &executorProcess{answer: func(context.Context, nodeproto.Request) (any, error) {
			return nodeproto.HelloResult{Protocol: protocol, SDK: nodeproto.SDKIdentity{Language: language, Version: version}, Features: []string{}}, nil
		}}
		err := sdkHello(t.Context(), &executorSession{process: process}, engineid.Identity{Pin: pin, Version: "installed"}, nodeproto.RunnerIdentity{Name: "local", Type: "local"}, nil)
		want := protocol == nodeproto.Version && language == "go" && version != "" && (replacement || strings.TrimPrefix(version, "v") == "1.0.0")
		if (err == nil) != want {
			t.Fatalf("hello (%d, %q, %q), replacement=%t: %v", protocol, language, version, replacement, err)
		}
	})
}
