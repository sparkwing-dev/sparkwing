package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type publicationCase struct {
	name, version, packedVersion, packedName, tag string
	metadata, status, versions, versionsStatus    string
	dryRunStatus, publishStatus, failure          string
	success, publish, preview                     bool
}

func TestTypeScriptPublicationRequiresAnExactImmutablePackage(t *testing.T) {
	for _, test := range []publicationCase{
		{name: "new", success: true, publish: true},
		{name: "same", metadata: "same", success: true},
		{name: "same latest retry", tag: "latest", metadata: "same", success: true},
		{name: "different", metadata: "different", failure: "published SDK bytes differ"},
		{name: "missing digest", metadata: `null`, failure: "published SDK bytes differ"},
		{name: "network", metadata: `{"error":{"code":"ENETUNREACH"}}`, status: "1", failure: "lookup failed"},
		{name: "auth", metadata: `{"error":{"code":"E401"}}`, status: "1", failure: "lookup failed"},
		{name: "invalid response", metadata: `not JSON`, status: "1", failure: "no valid JSON"},
		{name: "publish fails", publishStatus: "17", publish: true, failure: "publication failed"},
		{name: "dry run fails", dryRunStatus: "17", preview: true, failure: "dry run failed"},
		{name: "major one", version: "1.0.0", failure: "major version 0"},
		{name: "higher major prerelease", version: "22.0.0-rc.1", tag: "next", failure: "major version 0"},
		{name: "wrong packed version", packedVersion: "0.70.0", failure: "version must equal"},
		{name: "wrong package", packedName: "@other/sdk", failure: "name must be @sparkwing/sdk"},
		{name: "latest first publication", tag: "latest", versions: `{"error":{"code":"E404"}}`, versionsStatus: "1", success: true, publish: true},
		{name: "latest newer", tag: "latest", versions: `["0.9.0","0.70.9","0.72.0-rc.1"]`, success: true, publish: true},
		{name: "latest newer patch", version: "0.71.10", tag: "latest", versions: `["0.71.9"]`, success: true, publish: true},
		{name: "latest older minor", tag: "latest", versions: `["0.72.0","0.69.0"]`, failure: "latest requires"},
		{name: "latest older patch", version: "0.71.9", tag: "latest", versions: `["0.71.10"]`, failure: "latest requires"},
		{name: "latest equal", tag: "latest", versions: `["0.71.0"]`, failure: "latest requires"},
		{name: "versions network", tag: "latest", versions: `{"error":{"code":"ENETUNREACH"}}`, versionsStatus: "1", failure: "lookup failed"},
		{name: "versions auth", tag: "latest", versions: `{"error":{"code":"E401"}}`, versionsStatus: "1", failure: "lookup failed"},
		{name: "versions invalid JSON", tag: "latest", versions: `invalid`, failure: "no valid JSON"},
		{name: "versions invalid list", tag: "latest", versions: `null`, failure: "no valid version list"},
		{name: "versions invalid entry", tag: "latest", versions: `[42]`, failure: "no valid version list"},
		{name: "prerelease next", version: "0.72.0-rc.1", tag: "next", success: true, publish: true},
		{name: "prerelease latest", version: "0.72.0-rc.1", tag: "latest", failure: "prereleases must use"},
		{name: "prerelease other tag", version: "0.72.0-rc.1", failure: "prereleases must use"},
		{name: "older stable rebuild", versions: `["0.72.0"]`, success: true, publish: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkPublication(t, []byte("verified package bytes"), test)
		})
	}
}

func checkPublication(t *testing.T, payload []byte, test publicationCase) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("publish-typescript.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if test.version == "" {
		test.version = "0.71.0"
	}
	if test.packedVersion == "" {
		test.packedVersion = test.version
	}
	if test.packedName == "" {
		test.packedName = "@sparkwing/sdk"
	}
	if test.tag == "" {
		test.tag = "release-" + test.version
	}
	manifest, err := json.Marshal(map[string]string{"name": test.packedName, "version": test.packedVersion})
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		body []byte
	}{{"package/package.json", manifest}, {"package/index.js", payload}} {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if test.status == "" {
		test.status = "0"
	}
	switch test.metadata {
	case "":
		test.metadata, test.status = `{"error":{"code":"E404"}}`, "1"
	case "same":
		test.metadata = integrityMetadata(archive.Bytes())
	case "different":
		test.metadata = integrityMetadata(append(archive.Bytes(), 0))
	}
	if test.versions == "" {
		test.versions = `[]`
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "sdk.tgz")
	if err := os.WriteFile(file, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := "#!" + node + `
const { appendFileSync } = require("node:fs");
const args = process.argv.slice(2);
appendFileSync(process.env.CALLS, JSON.stringify(args) + "\n");
if (args[0] === "view") {
  const versions = args[1] === "@sparkwing/sdk";
  console.log(process.env[versions ? "VERSIONS" : "METADATA"]);
  process.exit(Number(process.env[versions ? "VERSIONS_STATUS" : "LOOKUP_STATUS"] || 0));
}
if (args[0] !== "publish") throw new Error("unexpected npm operation");
const dryRun = args.includes("--dry-run");
if (dryRun) console.log(JSON.stringify([{size: 1234, files: [{path: "package.json"}, {path: "index.js"}]}]));
process.exit(Number(process.env[dryRun ? "DRY_RUN_STATUS" : "PUBLISH_STATUS"] || 0));
`
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	command := exec.Command(node, script, file, test.version, test.tag)
	command.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "METADATA="+test.metadata, "LOOKUP_STATUS="+test.status, "VERSIONS="+test.versions, "VERSIONS_STATUS="+test.versionsStatus, "DRY_RUN_STATUS="+test.dryRunStatus, "PUBLISH_STATUS="+test.publishStatus, "CALLS="+calls)
	output, err := command.CombinedOutput()
	if (err == nil) != test.success || !strings.Contains(string(output), test.failure) {
		t.Fatalf("success=%v, want success=%v and failure=%q: %s", err == nil, test.success, test.failure, output)
	}
	body, err := os.ReadFile(calls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var publishes [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if line == "" {
			continue
		}
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		if args[0] == "publish" {
			publishes = append(publishes, args)
		} else if strings.Join(args, " ") != "view @sparkwing/sdk@"+test.version+" dist.integrity --json" && strings.Join(args, " ") != "view @sparkwing/sdk versions --json" {
			t.Fatalf("unexpected lookup: %v", args)
		}
	}
	var want [][]string
	args := []string{"publish", file, "--access", "public", "--tag", test.tag, "--ignore-scripts", "--provenance", "--json"}
	if test.publish || test.preview {
		want = append(want, append(append([]string{}, args...), "--dry-run"))
		if !strings.Contains(string(output), `"size":1234`) || !strings.Contains(string(output), `"path":"index.js"`) {
			t.Fatalf("dry run did not log packed size and files: %s", output)
		}
	}
	if test.publish {
		want = append(want, args)
	}
	gotJSON, _ := json.Marshal(publishes)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("npm publications %s, want %s", gotJSON, wantJSON)
	}
}

func integrityMetadata(data []byte) string {
	digest := sha512.Sum512(data)
	return strconv.Quote("sha512-" + base64.StdEncoding.EncodeToString(digest[:]))
}

func FuzzTypeScriptPublicationKeepsPackageBytesImmutable(f *testing.F) {
	f.Add([]byte("verified package"), true)
	f.Add([]byte{}, false)
	f.Fuzz(func(t *testing.T, payload []byte, same bool) {
		metadata := "different"
		if same {
			metadata = "same"
		}
		checkPublication(t, payload, publicationCase{metadata: metadata, success: same})
	})
}
