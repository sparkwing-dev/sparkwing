package protocolgo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

const (
	ChildHandlesName = "protocol-go-child-handles"
	ChildHandlesArg  = "--probe-child-handles"
)

type ChildHandleObservation struct {
	RequestOpen bool     `json:"request_open"`
	FrameOpen   bool     `json:"frame_open"`
	Env         []string `json:"env"`
}

func ObserveChildHandles(raw string) (ChildHandleObservation, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 2 {
		return ChildHandleObservation{}, fmt.Errorf("expected two protocol handles")
	}
	var open [2]bool
	for i, part := range parts {
		number, err := strconv.ParseUint(part, 10, strconv.IntSize)
		if err != nil {
			return ChildHandleObservation{}, err
		}
		open[i], err = childProtocolPipeOpen(uintptr(number))
		if err != nil {
			return ChildHandleObservation{}, err
		}
	}
	return ChildHandleObservation{RequestOpen: open[0], FrameOpen: open[1], Env: os.Environ()}, nil
}

type ChildHandlesPipeline struct{}

func (ChildHandlesPipeline) Plan(_ context.Context, plan *sparkwing.Plan, _ Inputs, _ sparkwing.RunContext) error {
	sparkwing.Job(plan, "child", childHandlesWork{})
	return nil
}

type childHandlesWork struct {
	sparkwing.Base
	sparkwing.Produces[ChildHandleObservation]
}

func (childHandlesWork) Work(work *sparkwing.Work) (*sparkwing.WorkStep, error) {
	return sparkwing.Step(work, "run", func(ctx context.Context) (ChildHandleObservation, error) {
		path, err := os.Executable()
		if err != nil {
			return ChildHandleObservation{}, err
		}
		pair := "3,4"
		if runtime.GOOS == "windows" {
			pair = os.Args[3]
		}
		output, err := exec.CommandContext(ctx, path, ChildHandlesArg, pair).CombinedOutput()
		if err != nil {
			return ChildHandleObservation{}, fmt.Errorf("child probe: %w: %s", err, output)
		}
		var observation ChildHandleObservation
		if err := json.Unmarshal(output, &observation); err != nil {
			return observation, err
		}
		sparkwing.Info(ctx, "child-exited")
		return observation, nil
	}), nil
}
