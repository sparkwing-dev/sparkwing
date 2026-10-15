package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

const goSDKModule = "github.com/sparkwing-dev/sparkwing"

// GoSDKLinkage lists the public SDK's linked packages and refuses host-owned dependencies.
func GoSDKLinkage(ctx context.Context) ([]string, error) {
	command := exec.CommandContext(ctx, "go", "list", "-deps", "-f", "{{.ImportPath}}", goSDKModule+"/sparkwing")
	command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false")
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list Go SDK imports: %w: %s", err, strings.TrimSpace(string(output)))
	}
	var linked []string
	for _, path := range strings.Fields(string(output)) {
		if strings.HasPrefix(path, "gopkg.in/yaml.") || strings.HasPrefix(path, "go.yaml.in/") ||
			strings.HasPrefix(path, "sigs.k8s.io/yaml") {
			return linked, fmt.Errorf("Go SDK links YAML implementation %s", path)
		}
		if !strings.HasPrefix(path, goSDKModule+"/") {
			continue
		}
		linked = append(linked, path)
		for _, forbidden := range []string{
			"/internal/blob", "/internal/cache", "/internal/cluster", "/internal/depcache",
			"/internal/engine", "/internal/nodehost", "/internal/orchestrator", "/internal/runners",
			"/internal/settings", "/internal/wingd", "/pkg/controller", "/pkg/store",
		} {
			if path == goSDKModule+forbidden || strings.HasPrefix(path, goSDKModule+forbidden+"/") {
				return linked, fmt.Errorf("Go SDK links host-owned package %s", path)
			}
		}
	}
	return linked, nil
}

// GoSDKProcessResult records protocol results produced by separate SDK processes.
type GoSDKProcessResult struct {
	Plan  nodeproto.PlanDoc
	Steps map[string]nodeproto.RunResult
}

// RunGoSDKProcessFixture executes the same pipeline used by the public fake-host comparison.
func RunGoSDKProcessFixture(ctx context.Context, routeOrigin string, jobs ...string) (GoSDKProcessResult, error) {
	if runtime.GOOS == "windows" {
		return GoSDKProcessResult{}, fmt.Errorf("fd3/fd4 process fixture requires Unix")
	}
	dir, err := os.MkdirTemp("", "sparkwing-go-process-*")
	if err != nil {
		return GoSDKProcessResult{}, err
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "pipeline")
	build := exec.CommandContext(ctx, "go", "build", "-p", "4", "-o", bin, goSDKModule+"/tests/acceptance/testdata/protocolgo/cmd")
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false")
	if output, err := build.CombinedOutput(); err != nil {
		return GoSDKProcessResult{}, fmt.Errorf("compile protocol fixture: %w: %s", err, strings.TrimSpace(string(output)))
	}
	result := GoSDKProcessResult{Steps: make(map[string]nodeproto.RunResult)}
	plan, _, err := runGoSDKSession(ctx, bin, dir, "", routeOrigin)
	if err != nil {
		return result, err
	}
	result.Plan = plan
	for _, job := range jobs {
		planned, step, err := runGoSDKSession(ctx, bin, dir, job, routeOrigin)
		if err != nil {
			return result, fmt.Errorf("%s session: %w", job, err)
		}
		if !equalPlanDoc(planned, plan) {
			return result, fmt.Errorf("%s session changed the declared plan", job)
		}
		result.Steps[job] = step
	}
	return result, nil
}

func equalPlanDoc(a, b nodeproto.PlanDoc) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func runGoSDKSession(ctx context.Context, bin, dir, job, routeOrigin string) (nodeproto.PlanDoc, nodeproto.RunResult, error) {
	requestR, requestW, err := os.Pipe()
	if err != nil {
		return nodeproto.PlanDoc{}, nodeproto.RunResult{}, err
	}
	frameR, frameW, err := os.Pipe()
	if err != nil {
		_ = requestR.Close()
		_ = requestW.Close()
		return nodeproto.PlanDoc{}, nodeproto.RunResult{}, err
	}
	defer requestW.Close()
	defer frameR.Close()
	command := exec.CommandContext(ctx, bin, protofd.Arg)
	command.Dir = dir
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "SPARKWING_") {
			command.Env = append(command.Env, item)
		}
	}
	command.ExtraFiles = []*os.File{requestR, frameW}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		_ = requestR.Close()
		_ = frameW.Close()
		return nodeproto.PlanDoc{}, nodeproto.RunResult{}, err
	}
	_ = requestR.Close()
	_ = frameW.Close()
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	reader := nodeproto.NewReader(frameR)
	hello := nodeproto.Request{
		ID: "hello", Op: nodeproto.OpHello, Protocol: nodeproto.Version,
		Engine: &nodeproto.EngineIdentity{Version: "fixture"},
		Runner: &nodeproto.RunnerIdentity{Name: "local", Type: "local", Labels: []string{"os=linux"}},
	}
	if job != "" {
		origin := routeOrigin
		if origin == "" {
			origin = "http://127.0.0.1:1"
		}
		hello.Binding = &nodeproto.ProcessBinding{
			RunID: "r", JobID: job,
			Routes: nodeproto.ProcessRoutes{URL: origin, Bearer: "cap-" + job},
		}
	}
	if err := sendGoSDKRequest(requestW, hello); err != nil {
		return nodeproto.PlanDoc{}, nodeproto.RunResult{}, err
	}
	reply, err := readGoSDKReply(reader, "hello")
	if err != nil {
		return nodeproto.PlanDoc{}, nodeproto.RunResult{}, err
	}
	var greeting nodeproto.HelloResult
	if err := reply.Decode(&greeting); err != nil {
		return nodeproto.PlanDoc{}, nodeproto.RunResult{}, fmt.Errorf("hello: %w", err)
	}
	request := nodeproto.Request{
		ID: "plan", Op: nodeproto.OpPlan, Pipeline: protocolgo.Name,
		Args: map[string]string{"name": "demo"},
		Run:  &nodeproto.RunContext{RunID: "r", Pipeline: protocolgo.Name},
	}
	if err := sendGoSDKRequest(requestW, request); err != nil {
		return nodeproto.PlanDoc{}, nodeproto.RunResult{}, err
	}
	reply, err = readGoSDKReply(reader, "plan")
	if err != nil {
		return nodeproto.PlanDoc{}, nodeproto.RunResult{}, err
	}
	var plan nodeproto.PlanDoc
	if err := reply.Decode(&plan); err != nil {
		return plan, nodeproto.RunResult{}, fmt.Errorf("plan: %w", err)
	}
	var step nodeproto.RunResult
	if job != "" {
		request = nodeproto.Request{ID: "step", Op: nodeproto.OpRunStep, Job: job, Step: "run", Attempt: 1}
		if err := sendGoSDKRequest(requestW, request); err != nil {
			return plan, step, err
		}
		reply, err = readGoSDKReply(reader, "step")
		if err != nil {
			return plan, step, err
		}
		if err := reply.Decode(&step); err != nil {
			return plan, step, fmt.Errorf("%s step: %w", job, err)
		}
	}
	if err := requestW.Close(); err != nil {
		return plan, step, err
	}
	err = command.Wait()
	waited = true
	if err != nil {
		return plan, step, fmt.Errorf("SDK process: %w: %s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		return plan, step, fmt.Errorf("SDK process wrote stdout: %s", stdout.String())
	}
	return plan, step, nil
}

func sendGoSDKRequest(pipe *os.File, request nodeproto.Request) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	_, err = pipe.Write(append(raw, '\n'))
	return err
}

func readGoSDKReply(reader *nodeproto.Reader, id string) (nodeproto.Reply, error) {
	for {
		line, err := reader.ReadLine()
		if err != nil {
			return nodeproto.Reply{}, err
		}
		frame, err := nodeproto.ParseFrame(line)
		if err != nil {
			return nodeproto.Reply{}, err
		}
		if frame.Reply != nil {
			if frame.Reply.ID != id {
				return nodeproto.Reply{}, fmt.Errorf("reply ID %q, want %q", frame.Reply.ID, id)
			}
			return *frame.Reply, nil
		}
	}
}
