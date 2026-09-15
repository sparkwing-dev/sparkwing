package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const releaseDockerFixture = `#!/usr/bin/env python3
import hashlib,json,os,sys
a=sys.argv[1:]
with open('docker-calls.jsonl','a') as f: f.write(json.dumps(a)+'\n')
state=json.load(open('registry.json')) if os.path.exists('registry.json') else {}
def option(name): return a[a.index(name)+1]
if a[:2]==['buildx','build']:
 assert option('--target')=='release'
 arch=option('--platform').split('/')[1]
 binary=next(x.split('=',1)[1] for x in a if x.startswith('BINARY='))
 assert open('dist/'+binary+'-linux-'+arch,'rb').read()==('payload:'+binary+':'+arch).encode()
 expected='build/Dockerfile.runner' if binary=='sparkwing-runner' else 'build/Dockerfile.binary'
 assert option('--file')==expected
 assert 'push-by-digest=true' in option('--output')
 assert '--tag' not in a
 digest='sha256:'+hashlib.sha256((binary+arch).encode()).hexdigest()
 json.dump({'containerimage.digest':digest},open(option('--metadata-file'),'w'))
elif a[:3]==['buildx','imagetools','inspect']:
 ref=a[-1]
 if '@sha256:' in ref: print(ref.split('@')[1])
 elif ref in state: print(state[ref])
 else: print('manifest unknown',file=sys.stderr); sys.exit(1)
elif a[:3]==['buildx','imagetools','create']:
 if '--dry-run' in a:
  assert len(a)==6
  print(json.dumps({'schemaVersion':2,'sources':a[-2:]}))
 else:
  tags=[a[i+1] for i,x in enumerate(a) if x=='--tag']
  sources=[];i=3
  while i<len(a):
   if a[i]=='--tag':i+=2
   else:sources.append(a[i]);i+=1
  if len(sources)==2:
   digest='sha256:'+hashlib.sha256(json.dumps({'schemaVersion':2,'sources':sources}).encode()).hexdigest()
   assert tags==[sources[0].split('@')[0]+'@'+digest]
  else: digest=sources[0].split('@')[1]
  for tag in tags:state[tag]=digest
  json.dump(state,open('registry.json','w'))
else: sys.exit('unexpected docker invocation')
`

func releaseFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(releaseDockerFixture), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, root
}

func runReleaseScript(t *testing.T, dir, root, name string, env ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(root, name))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{"PATH=" + dir + ":" + os.Getenv("PATH")}, env...)...)
	return cmd.CombinedOutput()
}

func TestReleaseImagesPackageBothArchitecturesWithoutRecompiling(t *testing.T) {
	dir, root := releaseFixture(t)
	event := filepath.Join(dir, "event.json")
	if err := os.WriteFile(event, []byte(`{"repository":{"name":"sparkwing","description":"fixture","license":{"spdx_id":"Elastic-2.0"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		for _, binary := range []string{"sparkwing-controller", "sparkwing-runner", "sparkwing-cache", "sparkwing-logs", "sparkwing-web"} {
			if err := os.WriteFile(filepath.Join(dir, "dist", binary+"-linux-"+arch), []byte("payload:"+binary+":"+arch), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		out, err := runReleaseScript(t, dir, root, "build-release-images.sh", "GOARCH="+arch, "TAG=v1.0.0", "SOURCE_SHA=fixture", "SPARKWING_IMAGE_REFRESH=fixture", "GITHUB_EVENT_PATH="+event)
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if out, err := runReleaseScript(t, dir, root, "assemble-image-digests.sh"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "scanned-image-digests"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("manifests=%d", len(entries))
	}
}

func TestReleaseImageTagsPreserveLatestAndImmutableVersions(t *testing.T) {
	for _, tc := range []struct {
		name, tag, latest     string
		existing, force, fail bool
	}{
		{"newest", "v1.10.0", "true", false, false, false},
		{"older finishes later", "v1.9.0", "false", false, false, false},
		{"immutable", "v1.9.0", "false", true, false, true},
		{"explicit force", "v1.9.0", "false", true, true, false},
		{"prerelease", "v2.0.0-rc.1", "false", false, false, false},
		{"prerelease latest refused", "v2.0.0-rc.1", "true", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, root := releaseFixture(t)
			if err := os.Mkdir(filepath.Join(dir, "scanned-image-digests"), 0o700); err != nil {
				t.Fatal(err)
			}
			state := map[string]string{}
			old := "sha256:" + strings.Repeat("a", 64)
			next := "sha256:" + strings.Repeat("b", 64)
			for _, binary := range []string{"sparkwing-controller", "sparkwing-runner", "sparkwing-cache", "sparkwing-logs", "sparkwing-web"} {
				if err := os.WriteFile(filepath.Join(dir, "scanned-image-digests", binary), []byte(next), 0o600); err != nil {
					t.Fatal(err)
				}
				image := "ghcr.io/sparkwing-dev/" + binary
				state[image+":latest"] = old
				if tc.existing {
					state[image+":"+tc.tag] = old
				}
			}
			body, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "registry.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			force := "false"
			if tc.force {
				force = "true"
			}
			out, err := runReleaseScript(t, dir, root, "publish-image-tags.sh", "TAG="+tc.tag, "LATEST="+tc.latest, "FORCE_RETAG="+force)
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v want failure=%v: %s", err, tc.fail, out)
			}
			body, err = os.ReadFile(filepath.Join(dir, "registry.json"))
			if err != nil {
				t.Fatal(err)
			}
			var after map[string]string
			if err := json.Unmarshal(body, &after); err != nil {
				t.Fatal(err)
			}
			for key, value := range after {
				if tc.fail && state[key] != value {
					t.Fatalf("refusal mutated %s", key)
				}
				if strings.HasSuffix(key, ":latest") {
					want := old
					if tc.latest == "true" && !tc.fail {
						want = next
					}
					if value != want {
						t.Fatalf("latest=%s want %s", value, want)
					}
				}
			}
		})
	}
}
