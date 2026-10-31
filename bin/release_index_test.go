package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestReleaseIndexWorkflowPublishesAfterReleaseAndSupportsRepair(t *testing.T) {
	body, err := os.ReadFile("../.github/workflows/release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			sdkReleaseJob `yaml:",inline"`
			Concurrency   struct {
				Group  string
				Cancel bool `yaml:"cancel-in-progress"`
				Queue  string
			}
		}
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["release-index"]
	if !hasSDKDependency(job.sdkReleaseJob, "release") || !hasSDKDependency(job.sdkReleaseJob, "validate-tag") || job.Allow {
		t.Fatal("index must require resolved release publication")
	}
	for _, clause := range []string{"always()", "!cancelled()", "!failure()", "!inputs.verify_only", "inputs.index_only", "needs.release.result == 'success'", "!contains(inputs.tag || github.ref_name, '-')"} {
		if !strings.Contains(job.If, clause) {
			t.Fatalf("missing dispatch guard %s", clause)
		}
	}
	if job.Concurrency.Group != "release-publication" || job.Concurrency.Cancel || job.Concurrency.Queue != "max" {
		t.Fatal("index updates must share the release publication queue")
	}
	if job.Permissions["id-token"] != "write" {
		t.Fatal("index publication needs AWS OIDC")
	}
	checkSDKAction(t, job.sdkReleaseJob, "aws-actions/configure-aws-credentials@", "role-to-assume", "${{ vars.SPARKWING_RELEASE_INDEX_ROLE_ARN }}")
	checkSDKAction(t, job.sdkReleaseJob, "actions/checkout@", "ref", "${{ github.event_name == 'workflow_dispatch' && github.sha || needs.validate-tag.outputs.source_sha }}")
	for _, step := range job.Steps {
		if step.Name != "Publish signed stable version index" {
			continue
		}
		if step.Allow || step.Run != "bash bin/publish-release-index.sh" || step.Env["SPARKWING_RELEASE_SIGNING_KEY"] != "${{ secrets.SPARKWING_UPDATE_SIGNING_KEY }}" {
			t.Fatal("index must use existing update signing authority and propagate failure")
		}
		return
	}
	t.Fatal("missing stable index publisher")
}

const releaseIndexToolStub = `#!/usr/bin/env bash
set -euo pipefail
name="${0##*/}"
printf '%s %s\n' "$name" "$*" >>"$FIXTURE/log"
case "$name" in
 go)
  [[ "${FAIL_GENERATE:-false}" != true ]] || exit 19
  while [[ "$1" != --index-out ]]; do shift; done
  cp "$FIXTURE/generated" "$2/versions.json"
  printf signature >"$2/versions.json.sig"
  ;;
 aws)
  case "$1 $2" in
   's3api list-objects-v2')
    [[ "${MISSING_CODE:-NoSuchKey}" != AccessDenied ]] || exit 1
    if [[ -f "$FIXTURE/remote/versions.json.sig" ]]; then echo 1; else echo 0; fi
    ;;
   's3api get-object')
    key="$6"; out="${!#}"
    if [[ "$key" == versions.json.sig && ! -f "$FIXTURE/remote/$key" ]]; then
     printf 'An error occurred (%s) when calling GetObject\n' "${MISSING_CODE:-NoSuchKey}" >&2
     exit 1
    fi
    cp "$FIXTURE/remote/$key" "$out"
    ;;
   's3api put-object')
    if [[ "${FAIL_UPLOAD:-false}" == true && "$6" == versions.json.sig ]]; then exit 23; fi
    cp "$8" "$FIXTURE/remote/$6"
    ;;
   'cloudfront create-invalidation') echo I123 ;;
   'cloudfront wait') ;;
   *) exit 29 ;;
  esac
  ;;
 curl)
  while [[ "$1" != https:* ]]; do shift; done
  key="${1##*/}"
  cp "$FIXTURE/remote/$key" "$3"
  ;;
esac
`

func TestReleaseIndexPublicationWritesOnlyVerifiedPairAndInvalidatesBoth(t *testing.T) {
	script, err := filepath.Abs("publish-release-index.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                                string
		bootstrap, signed, fail, failUpload bool
		code                                string
		want                                bool
	}{
		{name: "legacy bootstrap", bootstrap: true, want: true},
		{name: "signed refresh", signed: true, want: true},
		{name: "unsigned requires bootstrap"},
		{name: "permission denial is not missing", bootstrap: true, code: "AccessDenied"},
		{name: "failed generation writes nothing", signed: true, fail: true},
		{name: "interrupted upload retains repair pair", signed: true, failUpload: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, sub := range []string{"tools", "remote"} {
				if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, tool := range []string{"aws", "go", "curl"} {
				if err := os.WriteFile(filepath.Join(dir, "tools", tool), []byte(releaseIndexToolStub), 0700); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"remote/versions.json", "generated"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"latest":"v0.68.2"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.signed {
				if err := os.WriteFile(filepath.Join(dir, "remote/versions.json.sig"), []byte("old signature"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			flag := func(v bool) string {
				if v {
					return "true"
				}
				return "false"
			}
			cmd := exec.Command("bash", script)
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+dir, "FIXTURE="+dir, "TAG=v0.68.2", "BOOTSTRAP_INDEX="+flag(tc.bootstrap), "FAIL_GENERATE="+flag(tc.fail), "FAIL_UPLOAD="+flag(tc.failUpload), "MISSING_CODE="+tc.code)
			if tc.code == "" {
				cmd.Env = append(cmd.Env, "MISSING_CODE=NoSuchKey")
			}
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.want {
				t.Fatalf("success=%v want %v: %s", err == nil, tc.want, output)
			}
			log, err := os.ReadFile(filepath.Join(dir, "log"))
			if err != nil {
				t.Fatal(err)
			}
			calls := string(log)
			if tc.failUpload {
				for _, object := range []string{"versions.json", "versions.json.sig"} {
					retained, err := os.ReadFile(filepath.Join(dir, "release-index", object))
					if err != nil || len(retained) == 0 {
						t.Fatalf("lost repair artifact %s: %v", object, err)
					}
				}
				if strings.Contains(calls, "create-invalidation") {
					t.Fatal("invalidated incomplete pair")
				}
				return
			}
			if !tc.want {
				if strings.Contains(calls, "put-object") || strings.Contains(calls, "create-invalidation") {
					t.Fatalf("failed preflight wrote production: %s", calls)
				}
				return
			}
			if strings.Count(calls, "s3api put-object") != 2 || !strings.Contains(calls, "--paths /versions.json /versions.json.sig") || !strings.Contains(calls, "cloudfront wait invalidation-completed") {
				t.Fatalf("incomplete pair publication: %s", calls)
			}
			for _, line := range strings.Split(calls, "\n") {
				if strings.Contains(line, "put-object") && (!strings.Contains(line, "--bucket kikd-prod-sparkwing-website --key versions.json") || !strings.Contains(line, "max-age=60")) {
					t.Fatalf("wrong publication destination: %s", line)
				}
			}
		})
	}
}
