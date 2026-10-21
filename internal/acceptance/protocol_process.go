package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

// BuildGoSDKFixture builds the public SDK probe and returns its cleanup.
func BuildGoSDKFixture(ctx context.Context) (string, func(), error) {
	dir, err := os.MkdirTemp("", "sparkwing-go-process-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	bin := filepath.Join(dir, "pipeline")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := procenv.CommandContext(ctx, append(os.Environ(), "GOWORK=off", "GOFLAGS=-buildvcs=false"), "go", "build", "-p", "4", "-o", bin, goSDKModule+"/tests/acceptance/testdata/protocolgo/cmd")
	if output, err := build.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("compile protocol fixture: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return bin, cleanup, nil
}

// GoSDKProcess exposes the SDK's wire to an independent host fixture.
type GoSDKProcess struct {
	command *exec.Cmd
	request *os.File
	frame   *os.File
	frames  *nodeproto.Reader
	stdout  bytes.Buffer
	stderr  bytes.Buffer
}

// StartGoSDKProcess uses native inherited pipes without environment transport.
func StartGoSDKProcess(ctx context.Context, bin string) (*GoSDKProcess, error) {
	requestR, requestW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer requestR.Close()
	frameR, frameW, err := os.Pipe()
	if err != nil {
		_ = requestW.Close()
		return nil, err
	}
	defer frameW.Close()
	p := &GoSDKProcess{request: requestW, frame: frameR, frames: nodeproto.NewReader(frameR)}
	var environment []string
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "SPARKWING_") {
			environment = append(environment, item)
		}
	}
	p.command = procenv.CommandContext(ctx, environment, bin, protofd.Arg)
	p.command.Dir = filepath.Dir(bin)
	p.command.Stdout, p.command.Stderr = &p.stdout, &p.stderr
	err = goSDKCommandPipes(p.command, requestR, frameW)
	if err == nil {
		err = p.command.Start()
	}
	if err != nil {
		_ = requestW.Close()
		_ = frameR.Close()
		return nil, err
	}
	return p, nil
}

// Send writes a request without normalizing unknown or malformed wire fields.
func (p *GoSDKProcess) Send(request any) error { return json.NewEncoder(p.request).Encode(request) }

// NextFrame returns wire bytes so fixtures can judge errors independently of C2.
func (p *GoSDKProcess) NextFrame() ([]byte, error) { return p.frames.ReadLine() }

// Finish closes input and requires every requested reply to have been consumed.
func (p *GoSDKProcess) Finish() error {
	if err := p.request.Close(); err != nil {
		return err
	}
	if line, err := p.frames.ReadLine(); err != io.EOF {
		return fmt.Errorf("unexpected final frame %q: %v", line, err)
	}
	if err := p.command.Wait(); err != nil {
		return fmt.Errorf("SDK process: %w: %s", err, p.stderr.String())
	}
	return nil
}

// Stdout returns captured tool output after Finish or Close joins the process.
func (p *GoSDKProcess) Stdout() string { return p.stdout.String() }

// Stderr returns captured tool diagnostics after Finish or Close joins the process.
func (p *GoSDKProcess) Stderr() string { return p.stderr.String() }

// Close reaps the process even when a fixture failed before consuming its replies.
func (p *GoSDKProcess) Close() {
	_ = p.request.Close()
	_ = p.frame.Close()
	if p.command.ProcessState == nil {
		_ = p.command.Process.Kill()
		_ = p.command.Wait()
	}
}
