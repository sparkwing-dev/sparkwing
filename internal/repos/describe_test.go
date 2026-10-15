//go:build unix

package repos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
)

func fakeSDK(t *testing.T, body string) (bin, requests string) {
	t.Helper()
	dir := t.TempDir()
	bin, requests = filepath.Join(dir, "pipeline"), filepath.Join(dir, "requests")
	script := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --sw-node-protocol ] || exit 9\n" +
		"read -r line <&3\n" +
		"printf '%s\\n' '{\"reply\":\"1\",\"ok\":true,\"result\":{\"protocol\":2,\"sdk\":{\"language\":\"go\",\"version\":\"fixture\"},\"features\":[]}}' >&4\n" +
		"read -r line <&3\nprintf '%s\\n' \"$line\" > " + requests + "\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, requests
}

func TestDescribeReadsTheReplyPastLogRecords(t *testing.T) {
	bin, requests := fakeSDK(t, `printf '{"ts":"2026-10-12T00:00:00Z","msg":"planning"}\n' >&4
printf '{"reply":"2","ok":true,"result":{"pipelines":[{"name":"demo","args":[{"name":"env","type":"string","required":true}]}]}}\n' >&4`)
	doc, err := Describe(context.Background(), pipelineentry.Binary(bin), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pipelines) != 1 || doc.Pipelines[0].Name != "demo" || len(doc.Pipelines[0].Args) != 1 || !doc.Pipelines[0].Args[0].Required {
		t.Fatalf("pipelines = %+v", doc.Pipelines)
	}
	raw, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	req, err := nodeproto.ParseRequest([]byte(strings.TrimSpace(string(raw))))
	if err != nil || req.Op != nodeproto.OpDescribe {
		t.Fatalf("request = %+v, %v", req, err)
	}
}

func TestDescribeRefusesAnotherProtocolVersion(t *testing.T) {
	bin, _ := fakeSDK(t, `exit 0`)
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(raw), `"protocol":2`, `"protocol":1`, 1)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = Describe(context.Background(), pipelineentry.Binary(bin), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "invalid hello result") {
		t.Fatalf("err = %v, want a protocol refusal", err)
	}
}

func TestDescribeReturnsAFailedReply(t *testing.T) {
	bin, _ := fakeSDK(t, `printf '{"reply":"2","ok":false,"error":{"code":"plan_failed","message":"boom"}}\n' >&4`)
	_, err := Describe(context.Background(), pipelineentry.Binary(bin), t.TempDir())
	var perr *apierr.Error
	if !errors.As(err, &perr) || perr.Code != apierr.PlanFailed {
		t.Fatalf("err = %v, want the reply's plan_failed error", err)
	}
}

func TestDescribeReportsAnExitWithoutAReply(t *testing.T) {
	bin, _ := fakeSDK(t, `echo "registry init failed" >&2; exit 3`)
	_, err := Describe(context.Background(), pipelineentry.Binary(bin), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "exited without answering describe") || !strings.Contains(err.Error(), "registry init failed") {
		t.Fatalf("err = %v, want the exit and its stderr", err)
	}
}

func TestDescribeFailsAProtocolViolationAtOnce(t *testing.T) {
	for name, body := range map[string]string{
		"unknown reply id": `printf '{"reply":"3","ok":true,"result":{}}\n' >&4; exec sleep 300`,
		"garbage frame":    `printf 'not json\n' >&4; exec sleep 300`,
		// safety: the child stays blocked writing the rest of a line the host
		// stopped reading, which is the wait that must not happen.
		"oversized frame": `head -c 20000000 /dev/zero | tr '\0' a >&4; exec sleep 300`,
	} {
		t.Run(name, func(t *testing.T) {
			bin, _ := fakeSDK(t, body)
			_, err := Describe(context.Background(), pipelineentry.Binary(bin), t.TempDir())
			var perr *apierr.Error
			if !errors.As(err, &perr) || perr.Code != apierr.ProtocolError {
				t.Fatalf("err = %v, want protocol_error", err)
			}
		})
	}
}

func TestDescribeStopsAtTheDeadline(t *testing.T) {
	bin, _ := fakeSDK(t, `exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Describe(ctx, pipelineentry.Binary(bin), t.TempDir()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
}

func TestResolverDescribesOverTheProtocol(t *testing.T) {
	bin, _ := fakeSDK(t, `printf '{"reply":"2","ok":true,"result":{"pipelines":[{"name":"build"},{"name":""},{"name":"deploy"}]}}\n' >&4`)
	schemas, err := describePipelines(pipelineentry.Binary(bin), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pipelineNames(schemas), ","); got != "build,deploy" {
		t.Fatalf("names = %s", got)
	}
	bad, _ := fakeSDK(t, `exit 1`)
	if _, err := describePipelines(pipelineentry.Binary(bad), t.TempDir()); err == nil || !strings.Contains(err.Error(), bad) {
		t.Fatalf("err = %v, want it to name the binary", err)
	}
}
