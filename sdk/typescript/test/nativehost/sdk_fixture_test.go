package nativehost_test

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

type sdkFixture struct {
	node string
	root string
}

type sdkBuild struct {
	once    sync.Once
	fixture sdkFixture
	err     error
}

var nativeSDK sdkBuild

func (b *sdkBuild) load(ctx context.Context) (sdkFixture, error) {
	// perf: these tests execute the same immutable SDK output in separate protocol processes.
	b.once.Do(func() {
		b.fixture, b.err = buildSDK(ctx)
	})
	return b.fixture, b.err
}

func buildSDK(ctx context.Context) (sdkFixture, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return sdkFixture{}, err
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		return sdkFixture{}, err
	}
	build := exec.CommandContext(ctx, node, filepath.Join(root, "node_modules/typescript/bin/tsc"), "-p", filepath.Join(root, "tsconfig.build.json"))
	if output, err := build.CombinedOutput(); err != nil {
		return sdkFixture{}, fmt.Errorf("build SDK: %w\n%s", err, output)
	}
	return sdkFixture{node: node, root: root}, nil
}

func nativeSDKFixture(t *testing.T) (string, string) {
	t.Helper()
	fixture, err := nativeSDK.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return fixture.node, fixture.root
}
