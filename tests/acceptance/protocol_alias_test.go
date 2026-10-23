package acceptance_test

import (
	"encoding/json"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/acceptance"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

func testGoSDKEntrypointAlias(t *testing.T) {
	recordLeaf(t)
	const alias = "project-alias"
	bin, cleanup, err := acceptance.BuildGoSDKFixture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	process := startProtocolProbe(t, bin)
	sendProtocolProbe(t, process, probeHello())
	var hello nodeproto.HelloResult
	decodeProtocolProbe(t, protocolProbeReply(t, process, "hello"), &hello)
	sendProtocolProbe(t, process, nodeproto.Request{
		ID: "plan", Op: nodeproto.OpPlan,
		Pipeline: protocolgo.Name, Args: map[string]string{"name": "alias"},
		Run: &nodeproto.RunContext{RunID: "r", Pipeline: alias},
	})
	var planned nodeproto.PlanDoc
	decodeProtocolProbe(t, protocolProbeReply(t, process, "plan"), &planned)
	if err := process.Finish(); err != nil {
		t.Fatal(err)
	}
	protocolgo.Register()
	host := sparkwingtest.New(sparkwingtest.Options{Run: sparkwing.RunContext{RunID: "r", Pipeline: alias}})
	t.Cleanup(func() { _ = host.Close() })
	fake, err := host.Plan(t.Context(), protocolgo.Name, map[string]string{"name": "alias"})
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range []nodeproto.PlanDoc{planned, fake} {
		found := false
		for _, job := range doc.Jobs {
			found = found || job.ID == "invoked-"+alias
		}
		if !found {
			raw, _ := json.Marshal(doc)
			t.Fatalf("plan lost the invoked name: %s", raw)
		}
	}
}
