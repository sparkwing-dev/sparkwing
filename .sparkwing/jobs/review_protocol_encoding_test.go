package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	nodeproto "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	sdkserve "github.com/sparkwing-dev/sparkwing/sparkwing/runtime"
)

type reviewWireOperations struct {
	sdkserve.Operations
	run func(context.Context) error
}

func (o reviewWireOperations) Plan(context.Context, string, map[string]string, nodeproto.RunContext) (any, nodeproto.PlanDoc, error) {
	return struct{}{}, nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{}}, nil
}

func (o reviewWireOperations) RunStep(ctx context.Context, _ any, _ nodeproto.Request) (nodeproto.RunResult, error) {
	return nodeproto.RunResult{Outcome: "success"}, o.run(ctx)
}

// safety: Serve owns command encoding and decoding; a direct HostExec callback would miss byte loss in argv.
func reviewThroughProtocol(t *testing.T, run func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	requests, host := io.Pipe()
	replies, sdk := io.Pipe()
	served := make(chan error, 1)
	go func() {
		err := sdkserve.Serve(ctx, requests, sdk, sdkserve.Config{SDKVersion: "fixture", Operations: reviewWireOperations{Operations: sdkserve.DefaultOperations, run: run}})
		_ = sdk.CloseWithError(err)
		served <- err
	}()
	defer func() {
		cancel()
		_ = host.Close()
		_ = replies.Close()
		if err := <-served; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
		_ = sdk.Close()
	}()
	encode := json.NewEncoder(host)
	reader := nodeproto.NewReader(replies)
	requestsToSend := []nodeproto.Request{
		{ID: "hello", Op: nodeproto.OpHello, Protocol: nodeproto.HelloProtocol, Engine: &nodeproto.EngineIdentity{Version: "fixture"}, Runner: &nodeproto.RunnerIdentity{Name: "fixture", Type: "local", Labels: []string{}}, Binding: &nodeproto.ProcessBinding{RunID: "run", JobID: "review", Routes: nodeproto.ProcessRoutes{URL: "http://unused.invalid", Bearer: "fixture"}}},
		{ID: "plan", Op: nodeproto.OpPlan, Pipeline: "review", Args: map[string]string{}, Run: &nodeproto.RunContext{RunID: "run", Pipeline: "review"}},
		{ID: "run", Op: nodeproto.OpRunStep, Job: "review", Step: "run", Attempt: 1, Settled: []string{}},
	}
	commands := 0
	for _, request := range requestsToSend {
		if err := encode.Encode(request); err != nil {
			t.Fatal(err)
		}
		for {
			raw, err := reader.ReadLine()
			if err != nil {
				t.Fatal(err)
			}
			var command nodeproto.ExecRequest
			if err := json.Unmarshal(raw, &command); err != nil {
				t.Fatal(err)
			}
			if command.ID != "" {
				commands++
				for _, arg := range command.Argv {
					if strings.ContainsRune(arg, utf8.RuneError) {
						t.Errorf("JSON command replaced a filename byte: %q", arg)
					}
				}
				cmd := exec.CommandContext(ctx, command.Argv[0], command.Argv[1:]...)
				cmd.Dir, cmd.Env, cmd.Stdin = command.Dir, command.Env, bytes.NewReader(command.Stdin)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				runErr := cmd.Run()
				done := nodeproto.ExecDone{ID: command.ID}
				if cmd.ProcessState == nil {
					done.StartError = fmt.Sprint(runErr)
					done.ExitCode = -1
				} else {
					done.ExitCode = cmd.ProcessState.ExitCode()
				}
				for stream, data := range map[string][]byte{"stdout": stdout.Bytes(), "stderr": stderr.Bytes()} {
					if len(data) > 0 {
						if err := encode.Encode(nodeproto.ExecOutput{ID: command.ID, Stream: stream, Data: data}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := encode.Encode(done); err != nil {
					t.Fatal(err)
				}
				continue
			}
			var reply nodeproto.Reply
			if err := json.Unmarshal(raw, &reply); err != nil {
				t.Fatal(err)
			}
			if reply.ID == "" {
				continue
			}
			if reply.ID != request.ID || !reply.OK {
				t.Fatalf("protocol reply: %s", raw)
			}
			break
		}
	}
	if commands == 0 {
		t.Fatal("review never crossed the host command protocol")
	}
}

func TestReviewRawFilenamesCrossRealCommandProtocol(t *testing.T) {
	for _, kind := range []string{"text", "escaped", "binary"} {
		t.Run(kind, func(t *testing.T) {
			path := "internal/store/billing_\xff.go"
			before, after := "package store\n// deny\n", "package store\n// allow\n"
			if kind == "escaped" {
				after += "// \xfe\n"
			}
			if kind == "binary" {
				after += "\x00\xfe"
			}
			fx, root := gitEncodingReviewFixture(t, map[string]string{path: before}, map[string]string{path: after, "ordinary.md": "ordinary change\n"})
			for pass := range 2 {
				if pass == 1 {
					writeGoFile(t, filepath.Join(root, "follow-up.md"), "follow-up\n")
					gitCommitAll(t, root, "follow-up")
				}
				var rec reviewRecord
				reviewThroughProtocol(t, func(ctx context.Context) (err error) {
					rec, _, err = runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review byte-identical paths"})
					return err
				})
				for name, reviewer := range map[string]*fakeReviewer{"default": fx.def, "security": fx.sec} {
					if len(reviewer.calls) <= pass {
						t.Fatalf("%s pass%d was skipped", name, pass)
					}
					call := reviewer.calls[pass]
					if (call.session != "") != (pass == 1) {
						t.Errorf("%s pass%d wrong session", name, pass)
					}
					if !utf8.ValidString(call.prompt) || strings.ContainsRune(call.prompt, utf8.RuneError) {
						t.Errorf("%s lost byte identity", name)
					}
					if kind == "binary" {
						if !strings.Contains(call.prompt, `Binary file "internal/store/billing_\xff.go"`) || rec.Result != resultBlock {
							t.Errorf("%s omitted binary change or failed to block: %+v", name, rec)
						}
					} else if !strings.Contains(call.prompt, "-// deny\n+// allow") {
						t.Errorf("%s pass%d omitted security patch", name, pass)
					}
					if kind == "escaped" && !strings.Contains(call.prompt, "+// \\xfe") {
						t.Errorf("%s omitted escaped patch", name)
					}
					if name == "security" && strings.Contains(call.prompt, "+ordinary change") {
						t.Error("strict review included an unrelated path")
					}
				}
			}
		})
	}
}

func TestReviewMissingChangedDiffBlocksModelPass(t *testing.T) {
	for _, path := range []string{"internal/store/billing_\xff.go", "assets/opaque.png"} {
		t.Run(fmt.Sprintf("%q", path), func(t *testing.T) {
			fx, _ := gitEncodingReviewFixture(t, map[string]string{path: "old\n"}, map[string]string{path: "new\n"})
			git := fx.deps.git
			fx.deps.git = func(ctx context.Context, args ...string) (string, error) {
				if args[0] == "diff" && slices.Contains(args, "--text") {
					return "", nil
				}
				return git(ctx, args...)
			}
			var rec reviewRecord
			for pass := range 2 {
				reviewThroughProtocol(t, func(ctx context.Context) (err error) {
					rec, _, err = runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review missing diff"})
					return err
				})
				if rec.Result != resultBlock || rec.BlockingOpen == 0 {
					t.Fatalf("empty changed diff passed (cached=%t): %+v", pass == 1, rec)
				}
				if !slices.ContainsFunc(rec.Findings, func(f reviewFinding) bool {
					return f.File == path && strings.Contains(f.Summary, "changed file has no rendered diff")
				}) {
					t.Fatalf("missing mechanical coverage finding: %+v", rec)
				}
			}
		})
	}
}

func TestReviewProtocolRetainsBothTypeChangePatches(t *testing.T) {
	path := "internal/store/billing_type"
	fx, root := gitEncodingReviewFixture(t, map[string]string{path: "deny\n"}, map[string]string{path: "deny intermediate\n"})
	if err := os.Remove(filepath.Join(root, path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("allowed", filepath.Join(root, path)); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root, "change file type")
	raw := gitOutput(t, root, "diff", "--no-renames", "--text", "origin/main", "HEAD")
	if strings.Count(raw, "diff --git ") != 2 {
		t.Fatalf("fixture needs both type-change patches: %s", raw)
	}
	reviewThroughProtocol(t, func(ctx context.Context) error {
		_, _, err := runReview(ctx, fx.deps, testPolicy(t), reviewInputs{branch: "work", brief: "Review both file types"})
		return err
	})
	for name, rv := range map[string]*fakeReviewer{"default": fx.def, "security": fx.sec} {
		if len(rv.calls) != 1 {
			t.Fatalf("%s review was skipped", name)
		}
		for _, want := range []string{"deleted file mode 100644", "new file mode 120000", "-deny", "+allowed"} {
			if !strings.Contains(rv.calls[0].prompt, want) {
				t.Errorf("%s dropped type change %q", name, want)
			}
		}
	}
}
