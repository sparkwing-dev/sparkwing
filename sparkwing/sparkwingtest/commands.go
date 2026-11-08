package sparkwingtest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/hosthelpers"
	helperruntime "github.com/sparkwing-dev/sparkwing/internal/hosthelpers/runtime"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

func (h *host) stream(ctx context.Context, job, step string) func(string, []byte, bool) error {
	h.mu.Lock()
	h.commandSequence++
	key := fmt.Sprintf("test-command:%d:", h.commandSequence)
	h.mu.Unlock()
	return func(stream string, data []byte, eof bool) error {
		h.emit(ctx, nodeproto.FrameLog{TS: time.Now().UTC().Format(time.RFC3339Nano), Level: "info", Step: step, Event: "exec_line", Stream: key + stream, StreamData: base64.StdEncoding.EncodeToString(data), StreamEnd: eof}, job)
		return nil
	}
}

func (h *host) execute(ctx context.Context, job string, request nodeproto.ExecRequest) (sdkserve.HostExecResult, error) {
	if err := h.executionError(ctx); err != nil {
		return sdkserve.HostExecResult{}, err
	}
	if err := h.begin(ctx); err != nil {
		return sdkserve.HostExecResult{}, err
	}
	defer h.work.Done()
	var stdout, stderr bytes.Buffer
	output := h.stream(ctx, job, request.Step)
	if request.Capture {
		output = func(stream string, data []byte, _ bool) error {
			if stream == "stdout" {
				_, _ = stdout.Write(data)
			} else {
				_, _ = stderr.Write(data)
			}
			return nil
		}
	}
	done := h.commands.Execute(ctx, request, output)
	result := sdkserve.HostExecResult{Done: done, Stdout: done.Stdout, Stderr: done.Stderr}
	if request.Capture {
		result.Stdout, result.Stderr = stdout.Bytes(), stderr.Bytes()
	}
	return result, nil
}

type helperWriter func([]byte) (int, error)

func (write helperWriter) Write(data []byte) (int, error) { return write(data) }

func (s boundServices) Helper(ctx context.Context, request nodeproto.HelperRequest, out any) error {
	if err := s.executionError(ctx); err != nil {
		return err
	}
	if err := s.begin(ctx); err != nil {
		return err
	}
	defer s.work.Done()
	output := s.stream(ctx, s.job, request.Step)
	writer := func(stream string) helperWriter {
		return func(data []byte) (int, error) { return len(data), output(stream, data, false) }
	}
	ctx = s.helperCleanups.HelperContext(ctx)
	ctx = helperruntime.WithOutput(ctx, writer("stdout"), writer("stderr"))
	response := hosthelpers.Run(ctx, request)
	if err := output("stdout", nil, true); err != nil {
		return err
	}
	if err := output("stderr", nil, true); err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(response.Result, out); err != nil {
			return err
		}
	}
	if response.Error != "" {
		return &sdkserve.HelperError{Code: response.Code, Message: s.masker.Mask(response.Error)}
	}
	return nil
}
