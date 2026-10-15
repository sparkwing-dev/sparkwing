package orchestrator

import (
	"cmp"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func TestSubmissionEnvironmentIsOwnerOnlyAndDiscarded(t *testing.T) {
	home := t.TempDir()
	const runID = "run-environment"
	if err := CaptureSubmissionEnvironment(home, runID, []string{"SPARKWING_PROFILE=dev", "PATH=/submit/bin"}, quietLogger()); err != nil {
		t.Fatal(err)
	}

	path := submissionEnvironmentPath(home, runID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fssecure.VerifyPrivateConfig(path, info); err != nil {
		t.Fatalf("snapshot is not private: %v", err)
	}
	env := readSubmissionCapture(t, home, runID)
	if len(env) != 2 || env[0] != "SPARKWING_PROFILE=dev" || env[1] != "PATH=/submit/bin" {
		t.Fatalf("snapshot = %#v", env)
	}

	if err := DiscardSubmissionEnvironment(home, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("snapshot still exists after discard: %v", err)
	}
}

func TestCaptureSubmissionEnvironmentKeepsOnlyAllowedNonCredentialVariables(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow string
		env   []string
		want  []string
	}{
		{
			name: "keeps the dispatch snapshot shape",
			env:  []string{"SPARKWING_PROFILE=dev", "GITHUB_REF_NAME=main", "PATH=/bin", "HOME=/u", "AWS_REGION=us-east-1"},
			want: []string{"SPARKWING_PROFILE=dev", "GITHUB_REF_NAME=main", "PATH=/bin", "HOME=/u"},
		},
		{
			name: "carries the S3 endpoint and the config directory",
			env:  []string{"AWS_ENDPOINT_URL_S3=http://minio:9000", "AWS_ENDPOINT_URL=http://r2.example", "XDG_CONFIG_HOME=/u/.cfg", "AWS_PROFILE=x"},
			want: []string{"AWS_ENDPOINT_URL_S3=http://minio:9000", "AWS_ENDPOINT_URL=http://r2.example", "XDG_CONFIG_HOME=/u/.cfg"},
		},
		{
			name: "drops credential-shaped names",
			env:  []string{"GITHUB_TOKEN=gh", "SPARKWING_SECRETS_KEY=k", "AWS_SECRET_ACCESS_KEY=a", "SPARKWING_PROFILE=dev"},
			want: []string{"SPARKWING_PROFILE=dev"},
		},
		{
			name: "drops credential-shaped values",
			env:  []string{"SPARKWING_HEADER=Bearer abc", "SPARKWING_DB=postgres://u:p@h/db", "SPARKWING_PROFILE=dev"},
			want: []string{"SPARKWING_PROFILE=dev"},
		},
		{
			name:  "honors the operator allow-list",
			allow: "[AWS_REGION, DOCKER_*]",
			env: []string{
				"AWS_REGION=us-east-1", "DOCKER_HOST=tcp://h:1", "DOCKER_PASSWORD=p", "LANG=C",
			},
			want: []string{"AWS_REGION=us-east-1", "DOCKER_HOST=tcp://h:1"},
		},
		{
			name:  "carries the allow-listed agent socket and docker certificate directory",
			allow: "[SSH_AUTH_SOCK, DOCKER_CERT_PATH]",
			env: []string{
				"SSH_AUTH_SOCK=/tmp/agent.sock", "DOCKER_CERT_PATH=/home/u/.docker",
			},
			want: []string{"SSH_AUTH_SOCK=/tmp/agent.sock", "DOCKER_CERT_PATH=/home/u/.docker"},
		},
		{
			name: "drops a credential hidden in a URL query or path",
			env: []string{
				"SPARKWING_CACHE_URL=https://cache.example.com/?sig=abc",
				"SPARKWING_WEBHOOK=https://hooks.example.com/services/T0/zzzSECRET",
				"SPARKWING_HDR=x\nAuthorization: Bearer abc",
				"SPARKWING_SA={\"type\":\"service_account\",\"api_key\":\"a\"}",
				"SPARKWING_PROFILE=dev",
			},
			want: []string{"SPARKWING_PROFILE=dev"},
		},
		{
			name: "drops a personal access token named outside the substring set",
			env:  []string{"GITHUB_PAT=ghp_live", "SPARKWING_PROFILE=dev"},
			want: []string{"SPARKWING_PROFILE=dev"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeMachineSettings(t, "run:\n  submit_env_allow: "+cmp.Or(tc.allow, "[]")+"\n")
			home := t.TempDir()
			const runID = "run-filter"
			if err := CaptureSubmissionEnvironment(home, runID, tc.env, quietLogger()); err != nil {
				t.Fatal(err)
			}
			got := readSubmissionCapture(t, home, runID)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("captured environment = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestCaptureSubmissionEnvironmentRejectsABareWildcard(t *testing.T) {
	writeMachineSettings(t, "run:\n  submit_env_allow: [\"*\"]\n")
	err := CaptureSubmissionEnvironment(t.TempDir(), "run-wildcard",
		[]string{"AWS_REGION=us-east-1"}, quietLogger())
	if err == nil {
		t.Fatal("CaptureSubmissionEnvironment error = nil, want a rejection of the bare wildcard")
	}
	if !strings.Contains(err.Error(), submitEnvAllowKey) {
		t.Fatalf("error = %v, want it to name %s", err, submitEnvAllowKey)
	}
}

func readSubmissionCapture(t *testing.T, home, runID string) []string {
	t.Helper()
	body, err := os.ReadFile(submissionEnvironmentPath(home, runID))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot submissionEnvironmentSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.RunID != runID {
		t.Fatal("capture changed run identity")
	}
	return snapshot.Environment
}
