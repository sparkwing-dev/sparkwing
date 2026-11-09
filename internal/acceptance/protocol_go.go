package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	nodewire "github.com/sparkwing-dev/sparkwing/sparkwing/protocol"
	"github.com/sparkwing-dev/sparkwing/tests/acceptance/testdata/protocolgo"
)

const goSDKModule = "github.com/sparkwing-dev/sparkwing"

// GoSDKLinkage lists the public SDK's linked packages and refuses host-owned dependencies.
func GoSDKLinkage(ctx context.Context) ([]string, error) {
	command := procenv.ControlCommandContext(ctx, append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false"), "go", "list", "-deps", "-f", "{{.ImportPath}}", goSDKModule+"/sparkwing")
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
			"/internal/settings", "/internal/wingd", "/internal/controller", "/internal/store",
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
	Plan  nodewire.PlanDoc
	Steps map[string]nodewire.RunResult
}

// RunGoSDKProcessFixture executes the same pipeline used by the public fake-host comparison.
func RunGoSDKProcessFixture(ctx context.Context, routeOrigin string, jobs ...string) (GoSDKProcessResult, error) {
	bin, cleanup, err := BuildGoSDKFixture(ctx)
	if err != nil {
		return GoSDKProcessResult{}, err
	}
	defer cleanup()
	result := GoSDKProcessResult{Steps: make(map[string]nodewire.RunResult)}
	plan, _, err := runGoSDKSession(ctx, bin, "", routeOrigin)
	if err != nil {
		return result, err
	}
	result.Plan = plan
	for _, job := range jobs {
		planned, step, err := runGoSDKSession(ctx, bin, job, routeOrigin)
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

func equalPlanDoc(a, b nodewire.PlanDoc) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func runGoSDKSession(ctx context.Context, bin, job, routeOrigin string) (nodewire.PlanDoc, nodewire.RunResult, error) {
	process, err := StartGoSDKProcess(ctx, bin)
	if err != nil {
		return nodewire.PlanDoc{}, nodewire.RunResult{}, err
	}
	defer process.Close()
	requestW, reader := process.request, process.frames
	hello := nodewire.Request{
		ID: "hello", Op: nodewire.OpHello, Protocol: nodewire.Version,
		Engine: &nodewire.EngineIdentity{Version: "fixture"},
		Runner: &nodewire.RunnerIdentity{Name: "local", Type: "local", Labels: []string{"os=" + runtime.GOOS}},
	}
	if job != "" {
		origin := routeOrigin
		if origin == "" {
			origin = "http://127.0.0.1:1"
		}
		hello.Binding = &nodewire.ProcessBinding{
			RunID: "r", JobID: job,
			Routes: nodewire.ProcessRoutes{URL: origin, Bearer: "cap-" + job},
		}
	}
	if err := sendGoSDKRequest(requestW, hello); err != nil {
		return nodewire.PlanDoc{}, nodewire.RunResult{}, err
	}
	reply, err := readGoSDKReply(reader, "hello")
	if err != nil {
		return nodewire.PlanDoc{}, nodewire.RunResult{}, err
	}
	var greeting nodewire.HelloResult
	if err := reply.Decode(&greeting); err != nil {
		return nodewire.PlanDoc{}, nodewire.RunResult{}, fmt.Errorf("hello: %w", err)
	}
	request := nodewire.Request{
		ID: "plan", Op: nodewire.OpPlan, Pipeline: protocolgo.Name,
		Args: map[string]string{"name": "demo"},
		Run:  &nodewire.RunContext{RunID: "r", Pipeline: protocolgo.Name},
	}
	if err := sendGoSDKRequest(requestW, request); err != nil {
		return nodewire.PlanDoc{}, nodewire.RunResult{}, err
	}
	reply, err = readGoSDKReply(reader, "plan")
	if err != nil {
		return nodewire.PlanDoc{}, nodewire.RunResult{}, err
	}
	var plan nodewire.PlanDoc
	if err := reply.Decode(&plan); err != nil {
		return plan, nodewire.RunResult{}, fmt.Errorf("plan: %w", err)
	}
	var step nodewire.RunResult
	if job != "" {
		request = nodewire.Request{ID: "step", Op: nodewire.OpRunStep, Job: job, Step: "run", Attempt: 1, Settled: []string{}}
		if err := sendGoSDKRequest(requestW, request); err != nil {
			return plan, step, err
		}
		reply, err = readGoSDKReply(reader, "step")
		if err != nil {
			return plan, step, err
		}
		if !reply.OK {
			return plan, step, fmt.Errorf("%s invocation failed: %+v", job, reply.Error)
		}
		// safety: this fake host decodes the emitted body independently of the engine's error decoder.
		if err := json.Unmarshal(reply.Result, &step); err != nil {
			return plan, step, fmt.Errorf("%s step: %w", job, err)
		}
		if step.Outcome != "success" && step.Outcome != "failed" ||
			(step.Outcome == "failed") != (step.Error != nil) ||
			step.Error != nil && step.Error.Code == "" {
			return plan, step, fmt.Errorf("%s has an invalid body result", job)
		}
	}
	if err := process.Finish(); err != nil {
		return plan, step, err
	}
	if process.Stdout() != "" {
		return plan, step, fmt.Errorf("SDK process wrote stdout: %s", process.Stdout())
	}
	return plan, step, nil
}

func sendGoSDKRequest(pipe *os.File, request nodewire.Request) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	_, err = pipe.Write(append(raw, '\n'))
	return err
}

func readGoSDKReply(reader *nodewire.Reader, id string) (nodewire.Reply, error) {
	for {
		line, err := reader.ReadLine()
		if err != nil {
			return nodewire.Reply{}, err
		}
		frame, err := nodewire.ParseFrame(line)
		if err != nil {
			return nodewire.Reply{}, err
		}
		if frame.Reply != nil {
			if frame.Reply.ID != id {
				return nodewire.Reply{}, fmt.Errorf("reply ID %q, want %q", frame.Reply.ID, id)
			}
			return *frame.Reply, nil
		}
	}
}
