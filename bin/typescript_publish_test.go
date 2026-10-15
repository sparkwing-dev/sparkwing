package main

import (
	"crypto/sha512"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTypeScriptPublicationRequiresAnExactImmutablePackage(t *testing.T) {
	archive := []byte("verified package bytes")
	for _, test := range []publicationCase{
		{"new", `{"error":{"code":"E404"}}`, "1", "0", true, true},
		{"same", integrityMetadata(archive), "0", "0", true, false},
		{"different", `"sha512-other"`, "0", "0", false, false},
		{"missing digest", `null`, "0", "0", false, false},
		{"network", `{"error":{"code":"ENETUNREACH"}}`, "1", "0", false, false},
		{"auth", `{"error":{"code":"E401"}}`, "1", "0", false, false},
		{"invalid response", `not JSON`, "1", "0", false, false},
		{"publish fails", `{"error":{"code":"E404"}}`, "1", "17", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkPublication(t, archive, test)
		})
	}
}

type publicationCase struct {
	name, metadata, status, publishStatus string
	success, publish                      bool
}

func checkPublication(t *testing.T, archive []byte, test publicationCase) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("publish-typescript.mjs")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "sdk.tgz")
	if err := os.WriteFile(file, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	fake := `import childProcess from "node:child_process";
import { writeFileSync } from "node:fs";
import { syncBuiltinESMExports } from "node:module";
childProcess.spawnSync = (command, args) => {
  if (command !== "npm") throw new Error("unexpected command");
  if (args[0] === "view") {
    if (JSON.stringify(args) !== JSON.stringify(["view", "@sparkwing/sdk@0.71.0", "dist.integrity", "--json"])) throw new Error("wrong package lookup");
    return { status: Number(process.env.LOOKUP_STATUS), stdout: process.env.METADATA };
  }
  if (args[0] !== "publish") throw new Error("unexpected npm operation");
  writeFileSync(process.env.CALLS, args.join("\n") + "\n");
  return { status: Number(process.env.PUBLISH_STATUS) };
};
syncBuiltinESMExports();
`
	preload := "data:text/javascript;base64," + base64.StdEncoding.EncodeToString([]byte(fake))
	calls := filepath.Join(dir, "calls")
	command := exec.Command(node, "--import", preload, script, file, "0.71.0", "release-0.71.0")
	command.Env = append(os.Environ(), "METADATA="+test.metadata, "LOOKUP_STATUS="+test.status, "PUBLISH_STATUS="+test.publishStatus, "CALLS="+calls)
	output, err := command.CombinedOutput()
	if (err == nil) != test.success {
		t.Fatalf("success=%v: %s", err == nil, output)
	}
	body, err := os.ReadFile(calls)
	if test.publish {
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Join([]string{"publish", file, "--access", "public", "--tag", "release-0.71.0", "--ignore-scripts", ""}, "\n")
		if string(body) != want {
			t.Fatalf("npm invocation %q", body)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal("lookup failure or existing package triggered publication")
	}
}

func integrityMetadata(data []byte) string {
	digest := sha512.Sum512(data)
	return strconv.Quote("sha512-" + base64.StdEncoding.EncodeToString(digest[:]))
}

func FuzzTypeScriptPublicationKeepsPackageBytesImmutable(f *testing.F) {
	f.Add([]byte("verified package"), true)
	f.Add([]byte{}, false)
	f.Fuzz(func(t *testing.T, archive []byte, same bool) {
		recorded := archive
		if !same {
			recorded = append(append([]byte{}, archive...), 0)
		}
		checkPublication(t, archive, publicationCase{metadata: integrityMetadata(recorded), status: "0", publishStatus: "0", success: same})
	})
}
