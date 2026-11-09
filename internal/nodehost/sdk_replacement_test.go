package nodehost

import (
	"context"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
)

func FuzzReplacementHelloPreservesProtocolAndLanguage(f *testing.F) {
	f.Add(nodewire.Version, "go", "(devel)", true)
	f.Add(nodewire.Version, "go", "v1.0.0", false)
	f.Add(nodewire.Version+1, "typescript", "anything", true)
	f.Fuzz(func(t *testing.T, protocol int, language, version string, replacement bool) {
		pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v1.0.0"}
		if replacement {
			pin.Replacement = "../sdk"
		}
		process := &executorProcess{answer: func(context.Context, nodewire.Request) (any, error) {
			return nodewire.HelloResult{Protocol: protocol, SDK: nodewire.SDKIdentity{Language: language, Version: version}}, nil
		}}
		err := sdkHello(t.Context(), &executorSession{process: process}, engineid.Identity{Pin: pin, Version: "installed"}, nodewire.RunnerIdentity{Name: "local", Type: "local"}, nil)
		want := protocol == nodewire.Version && language == "go" && version != "" && (replacement || strings.TrimPrefix(version, "v") == "1.0.0")
		if (err == nil) != want {
			t.Fatalf("hello (%d, %q, %q), replacement=%t: %v", protocol, language, version, replacement, err)
		}
	})
}
