package orchestrator_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestProcessPerNode_S3StateRunsEveryNodeInItsOwnProcess(t *testing.T) {
	mod, bin := buildProcPerNodeBinary(t)
	endpoint, s3client, bucket := fakeBucket(t)

	home := t.TempDir()
	stopHomeDaemon(t, home)
	probe := t.TempDir()

	profiles := filepath.Join(t.TempDir(), "profiles.yaml")
	writeMod(t, profiles, fmt.Sprintf(""+
		"profiles:\n"+
		"  modetwo:\n"+
		"    mirror_local: false\n"+
		"    state: { type: s3, bucket: %s, prefix: state }\n"+
		"    logs:  { type: s3, bucket: %s, prefix: logs }\n", bucket, bucket))

	runEnv := append(os.Environ(),
		"SPARKWING_HOME="+home,
		"SPARKWING_WINGD_BIN="+wingdHostBin(t),
		"SPARKWING_LOG_FORMAT=json",
		"PROC_PROBE_DIR="+probe,
		"SPARKWING_PROFILES="+profiles,
		"SPARKWING_PROFILE=modetwo",
		"SPARKWING_S3_ENDPOINT="+endpoint,
		"AWS_REGION=us-east-1",
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",

		"AWS_PROFILE=",
		"AWS_SHARED_CREDENTIALS_FILE="+filepath.Join(t.TempDir(), "credentials"),
		"AWS_CONFIG_FILE="+filepath.Join(t.TempDir(), "config"),
	)

	out := runBin(t, mod, runEnv, bin, "spawnproof")

	dispatcher := readPID(t, probe, "dispatcher")
	for _, node := range []string{"produce", "consume", "recover"} {
		pid := readPID(t, probe, node)
		if pid == dispatcher {
			t.Errorf("node %q ran in the dispatcher's process (%d); object-store state is still the in-process path",
				node, pid)
		}
	}
	if readPID(t, probe, "produce") == readPID(t, probe, "consume") {
		t.Error("produce and consume shared a process")
	}
	if !strings.Contains(out, "consumed digest=sha-abc123") {
		t.Errorf("consumer did not read the producer's typed output across the process boundary:\n%s", out)
	}

	if _, err := os.Stat(filepath.Join(home, "state.db")); err == nil {
		t.Error("a mirror_local:false object-store run wrote a local state database")
	}

	run, nodes := readRunFromBucket(t, s3client, bucket, "spawnproof")
	if run.Status != "success" {
		t.Errorf("run status in the bucket = %q (err=%q), want success", run.Status, run.Error)
	}
	for _, id := range []string{"produce", "consume"} {
		n, ok := nodes[id]
		if !ok {
			t.Errorf("node %q missing from the bucket's run record", id)
			continue
		}
		if n.Outcome != string(sparkwing.Success) {
			t.Errorf("node %q outcome in the bucket = %q, want success", id, n.Outcome)
		}
	}
	if produce := nodes["produce"]; produce != nil && string(produce.Output) != `{"digest":"sha-abc123"}` {
		t.Errorf("produce output in the bucket = %s", produce.Output)
	}

	assertNodeLogInBucket(t, s3client, bucket, run.ID, "consume", "consumed digest=sha-abc123")
}

func assertNodeLogInBucket(t *testing.T, client *awss3.Client, bucket, runID, nodeID, want string) {
	t.Helper()
	logs := s3store.NewLogStore(bucket, "logs", client)
	body, err := logs.Read(context.Background(), runID, nodeID, storage.ReadOpts{})
	if err != nil {
		t.Fatalf("read %s/%s log from the bucket: %v", runID, nodeID, err)
	}
	if !strings.Contains(string(body), want) {
		t.Errorf("node %s log in the bucket does not carry %q:\n%s", nodeID, want, body)
	}
}

func fakeBucket(t *testing.T) (endpoint string, client *awss3.Client, bucket string) {
	t.Helper()
	srv := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(srv.Close)

	client = awss3.New(awss3.Options{
		Region:             "us-east-1",
		BaseEndpoint:       aws.String(srv.URL),
		UsePathStyle:       true,
		Credentials:        credentials.NewStaticCredentialsProvider("test", "test", ""),
		EndpointResolverV2: awss3.NewDefaultEndpointResolverV2(),
	})
	bucket = "sw-ppn-" + bucketSafeSuffix()
	if _, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(bucket),
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return srv.URL, client, bucket
}

func readRunFromBucket(t *testing.T, client *awss3.Client, bucket, pipeline string) (*store.Run, map[string]*store.Node) {
	t.Helper()
	ctx := context.Background()
	art := s3store.NewArtifactStore(bucket, "state", client)
	keys, err := art.List(ctx, "runs/")
	if err != nil {
		t.Fatalf("list run state objects: %v", err)
	}
	var stateKey string
	for _, k := range keys {
		if strings.HasSuffix(k, "/state.ndjson") {
			stateKey = k
			break
		}
	}
	if stateKey == "" {
		t.Fatalf("no run state object under runs/ (saw %v)", keys)
	}

	rc, err := art.Get(ctx, stateKey)
	if err != nil {
		t.Fatalf("read %s: %v", stateKey, err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s body: %v", stateKey, err)
	}

	var run *store.Run
	nodes := map[string]*store.Node{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var env struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal([]byte(line), &env) != nil {
			continue
		}
		switch env.Kind {
		case "run":
			var r store.Run
			if json.Unmarshal(env.Data, &r) == nil && r.Pipeline == pipeline {
				run = &r
			}
		case "node":
			var n store.Node
			if json.Unmarshal(env.Data, &n) == nil {
				nodes[n.NodeID] = &n
			}
		}
	}
	if run == nil {
		t.Fatalf("no run record for %s in %s", pipeline, stateKey)
	}
	return run, nodes
}
