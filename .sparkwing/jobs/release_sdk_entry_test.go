package jobs

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoSDKEntryUsesPublishedSDKTagForOlderEngineSource(t *testing.T) {
	producer, err := filepath.Abs("../../bin/go-sdk-release-entry.mjs")
	if err != nil {
		t.Fatal(err)
	}
	repo := sdkFixture(t)
	sdkFixtureWrite(t, repo, "sparkwing/protocol/protocol_contract.go", "package protocol\nconst Version = 2\n")
	sdkFixtureCommit(t, repo)
	digest, err := sdkSourceDigest(newTestContext(t), repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	sdkFixtureTag(t, repo, "v0.1.0", digest)
	sdkFixtureWrite(t, repo, "go.mod", "module fixture/engine\n\ngo 1.26.0\nrequire "+sdkModulePath+" v0.1.0\n")
	sdkFixtureWrite(t, repo, "sparkwing/protocol/protocol_contract.go", "package protocol\nconst Version = 99\n")
	sdkFixtureCommit(t, repo)
	output := filepath.Join(t.TempDir(), "sdk-entry-go.json")
	run := func() ([]struct {
		Language     string `json:"language"`
		Version      string `json:"version"`
		Protocol     int    `json:"protocol"`
		SourceDigest string `json:"source_digest"`
	}, error,
	) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "node", producer, repo, output)
		if body, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("producer: %v\n%s", err, body)
		}
		body, err := os.ReadFile(output)
		if err != nil {
			return nil, err
		}
		var entries []struct {
			Language     string `json:"language"`
			Version      string `json:"version"`
			Protocol     int    `json:"protocol"`
			SourceDigest string `json:"source_digest"`
		}
		if err := json.Unmarshal(body, &entries); err != nil {
			return nil, err
		}
		return entries, nil
	}
	entries, err := run()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Language != "go" || entries[0].Version != "v0.1.0" || entries[0].Protocol != 2 || entries[0].SourceDigest != digest {
		t.Fatalf("SDK entry = %+v", entries)
	}
	sdkFixtureWrite(t, repo, "go.mod", "module fixture/legacy-engine\n\ngo 1.26.0\n")
	entries, err = run()
	if err != nil || len(entries) != 0 {
		t.Fatalf("older engine with no independent SDK = %+v, %v", entries, err)
	}
	sdkFixtureWrite(t, repo, "go.mod", "module fixture/engine\n\ngo 1.26.0\nrequire "+sdkModulePath+" v0.2.0\n")
	cmd := exec.CommandContext(t.Context(), "node", producer, repo, output)
	if body, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(body), "sparkwing/v0.2.0") {
		t.Fatalf("missing SDK tag = %v\n%s", err, body)
	}
}
