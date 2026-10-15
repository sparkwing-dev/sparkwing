package nodehost

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

func TestV2SessionRequiresGrantedIdentity(t *testing.T) {
	for _, missing := range []string{"engine", "name", "type", "labels"} {
		t.Run(missing, func(t *testing.T) {
			cfg := SessionConfig{Entry: pipelineentry.Binary("/missing/sdk"), Engine: nodeproto.EngineIdentity{Version: "test"}, Runner: nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{}}}
			switch missing {
			case "engine":
				cfg.Engine.Version = ""
			case "name":
				cfg.Runner.Name = ""
			case "type":
				cfg.Runner.Type = ""
			case "labels":
				cfg.Runner.Labels = nil
			}
			_, err := StartSession(t.Context(), cfg)
			if err == nil || err.Error() != "sdk session needs the selected engine version and granted runner identity" {
				t.Fatalf("missing %s identity: %v", missing, err)
			}
		})
	}
}

func FuzzV2SessionRefusesIncompleteIdentity(f *testing.F) {
	f.Add("test", "runner", "cloud", uint8(0))
	f.Fuzz(func(t *testing.T, engine, name, kind string, missing uint8) {
		cfg := SessionConfig{Entry: pipelineentry.Binary("/missing/sdk"), Engine: nodeproto.EngineIdentity{Version: engine}, Runner: nodeproto.RunnerIdentity{Name: name, Type: kind, Labels: []string{}}}
		switch missing % 4 {
		case 0:
			cfg.Engine.Version = ""
		case 1:
			cfg.Runner.Name = ""
		case 2:
			cfg.Runner.Type = ""
		case 3:
			cfg.Runner.Labels = nil
		}
		_, err := StartSession(t.Context(), cfg)
		if err == nil || err.Error() != "sdk session needs the selected engine version and granted runner identity" {
			t.Fatalf("incomplete identity reached process startup: %v", err)
		}
	})
}

func TestV2SDKProcess(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_V2_SDK") != "1" {
		return
	}
	requests := nodeproto.NewReader(os.NewFile(3, "requests"))
	frames := nodeproto.NewWriter(os.NewFile(4, "frames"))
	hello, err := requests.NextRequest()
	if err != nil || hello.Op != nodeproto.OpHello {
		os.Exit(21)
	}
	for _, text := range append(os.Environ(), os.Args...) {
		if strings.Contains(text, hello.Binding.Routes.Bearer) {
			os.Exit(22)
		}
	}
	result, _ := json.Marshal(nodeproto.HelloResult{Protocol: 2, SDK: nodeproto.SDKIdentity{Language: "go", Version: "v1.2.3"}, Features: []string{}})
	if frames.Send(nodeproto.Reply{ID: hello.ID, OK: true, Result: result}) != nil {
		os.Exit(23)
	}
	for {
		req, err := requests.NextRequest()
		if err != nil {
			os.Exit(0)
		}
		result, _ := json.Marshal(hello)
		if frames.Send(nodeproto.Reply{ID: req.ID, OK: true, Result: result}) != nil {
			os.Exit(24)
		}
	}
}

type noV2Secrets struct{}

func (noV2Secrets) ContainsSecret(string) bool { return false }
