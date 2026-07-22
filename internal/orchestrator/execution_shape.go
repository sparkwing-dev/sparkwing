package orchestrator

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"runtime"
	"sort"

	"github.com/fxamacker/cbor/v2"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const executionShapeSchema = "sparkwing.execution-shape.v1\x00"

type executionShape struct {
	NodeID       string
	Dependencies []string
	ActionType   string
	Action       []byte
	Environment  map[string]string
	Modifiers    *snapshotModifiers
	Work         *snapshotWork
	Platform     string
	Architecture string
	Toolchain    string
}

func executionShapeHash(node *sparkwing.JobNode) string {
	deps := append([]string(nil), node.DepIDs()...)
	sort.Strings(deps)
	var action []byte
	if encoded, err := executionShapeCBOR().Marshal(node.Job()); err == nil {
		action = encoded
	}
	var work *snapshotWork
	if node.Work() != nil {
		work, _ = newWorkWalker().walk(node.Work(), node.ResultStep())
	}
	shape := executionShape{
		NodeID:       node.ID(),
		Dependencies: deps,
		ActionType:   reflect.TypeOf(node.Job()).String(),
		Action:       action,
		Environment:  node.EnvMap(),
		Modifiers:    nodeModifiersSnapshot(node),
		Work:         work,
		Platform:     runtime.GOOS,
		Architecture: runtime.GOARCH,
		Toolchain:    runtime.Version(),
	}
	payload, err := executionShapeCBOR().Marshal(shape)
	if err != nil {
		panic(err)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(executionShapeSchema))
	_, _ = digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil))
}

func executionShapeCBOR() cbor.EncMode {
	mode, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	return mode
}

func executionShapeHashes(nodes []*sparkwing.JobNode) map[string]string {
	hashes := make(map[string]string, len(nodes))
	for _, node := range nodes {
		hashes[node.ID()] = executionShapeHash(node)
	}
	return hashes
}
