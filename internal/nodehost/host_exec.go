package nodehost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// hostCommand is one SDK command the host runs for its session.
type hostCommand struct {
	kill     chan struct{}
	killOnce sync.Once
}

func (c *hostCommand) stop() { c.killOnce.Do(func() { close(c.kill) }) }

// commandDrainGrace bounds how long output may trail the command's exit, so a
// background child holding the pipes cannot hold the command open.
const commandDrainGrace = 500 * time.Millisecond

// startExec runs an SDK command without blocking the frame reader, which must keep
// draining the SDK's log frames while the command writes.
func (s *protocolSession) startExec(req nodeproto.ExecRequest) {
	step := s.attributeProtocolLog(nodeproto.FrameLog{Step: req.Step}).Step
	command := &hostCommand{kill: make(chan struct{})}
	s.mu.Lock()
	if s.failure != nil {
		s.mu.Unlock()
		return
	}
	if s.execs == nil {
		s.execs = map[string]*hostCommand{}
	}
	if _, running := s.execs[req.ID]; running {
		s.mu.Unlock()
		s.failProtocol(protocolSessionError(apierr.ProtocolError, "SDK reused a running command id"))
		return
	}
	s.execs[req.ID] = command
	s.execSeq++
	key := fmt.Sprintf("exec:%d:", s.execSeq)
	s.commands.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.commands.Done()
		var sink func(stream string, chunk []byte, eof bool) error
		if req.Capture {
			sink = func(stream string, chunk []byte, eof bool) error {
				if eof {
					return nil
				}
				return s.writeHostLine(nodeproto.ExecOutput{ID: req.ID, Stream: stream, Data: chunk})
			}
		} else {
			sink = func(stream string, chunk []byte, eof bool) error {
				if s.cfg.Activity.Progress != nil {
					s.cfg.Activity.Progress()
				}
				base := nodeproto.FrameLog{TS: time.Now().UTC().Format(time.RFC3339Nano), Level: "info", Step: step, Event: "exec_line"}
				return s.cfg.output.stream(s.logCtx, key+stream, base, chunk, eof)
			}
		}
		done, session := runHostCommand(req, command.kill, s.done, sink)
		s.mu.Lock()
		delete(s.execs, req.ID)
		if session != nil {
			s.commandSessions = append(s.commandSessions, session)
		}
		s.mu.Unlock()
		if err := s.writeHostLine(done); err != nil {
			s.failProtocol(err)
		}
	}()
}

func (s *protocolSession) cancelExec(id string) {
	s.mu.Lock()
	command := s.execs[id]
	s.mu.Unlock()
	// safety: a cancel can cross the command's own completion on the wire.
	if command != nil {
		command.stop()
	}
}

// writeHostLine shares the request pipe with host requests; the SDK routes these
// lines by their leading key.
func (s *protocolSession) writeHostLine(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.requests.Write(append(data, '\n')); err != nil {
		if s.protocolFailure() != nil {
			return nil
		}
		return protocolSessionError(apierr.ProtocolError, "write SDK command result failed")
	}
	return nil
}

var commandBuffers = sync.Pool{New: func() any { return new([64 << 10]byte) }}

// runHostCommand starts req in its own session and feeds each output stream to
// sink, ending each with eof. A logged command keeps only bounded tails for the
// SDK's error messages. Either kill or closed ends the command's session; the
// returned session outlives a normal exit until the SDK session ends.
func runHostCommand(req nodeproto.ExecRequest, kill, closed <-chan struct{}, sink func(stream string, chunk []byte, eof bool) error) (nodeproto.ExecDone, *commandSession) {
	done := nodeproto.ExecDone{ID: req.ID, ExitCode: -1}
	if info, err := os.Stat(req.Dir); err != nil {
		done.StartError = fmt.Sprintf("dir %q: %v", req.Dir, err)
		return done, nil
	} else if !info.IsDir() {
		done.StartError = fmt.Sprintf("dir %q: not a directory", req.Dir)
		return done, nil
	}
	path := req.Path
	if path == "" {
		var err error
		if path, err = lookCommand(req.Argv[0], req.Env, req.Dir); err != nil {
			done.StartError, done.NotFound = err.Error(), errors.Is(err, exec.ErrNotFound)
			return done, nil
		}
	}
	cmd := procenv.SDKCommand(req.Env, path, req.Argv[1:]...)
	cmd.Args[0], cmd.Dir = req.Argv[0], req.Dir
	if req.Stdin != nil {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}
	// safety: a background child holding stdin would otherwise keep Wait's copier, and the session, alive.
	cmd.WaitDelay = commandDrainGrace
	configureHostCommand(cmd)
	outR, outW, err := os.Pipe()
	if err != nil {
		done.StartError = err.Error()
		return done, nil
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeFiles(outR, outW)
		done.StartError = err.Error()
		return done, nil
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	select {
	case <-kill:
		closeFiles(outR, outW, errR, errW)
		done.StartError = "command cancelled before it started"
		return done, nil
	case <-closed:
		closeFiles(outR, outW, errR, errW)
		done.StartError = "SDK session ended before the command started"
		return done, nil
	default:
	}
	if err := cmd.Start(); err != nil {
		closeFiles(outR, outW, errR, errW)
		done.StartError = err.Error()
		return done, nil
	}
	// safety: the child owns the write ends now; dropping ours lets the readers see EOF.
	closeFiles(outW, errW)
	session := trackHostCommand(cmd, req.Env)
	exited := make(chan struct{})
	go func() {
		select {
		case <-kill:
			session.kill()
		case <-closed:
			session.kill()
		case <-exited:
		}
	}()
	tails := [2]*commandTail{{keep: !req.Capture}, {keep: !req.Capture}}
	var sinkMu sync.Mutex
	var sinkErr error
	var drains sync.WaitGroup
	for i, r := range []*os.File{outR, errR} {
		stream := [2]string{"stdout", "stderr"}[i]
		drains.Add(1)
		go func() {
			defer drains.Done()
			pooled := commandBuffers.Get().(*[64 << 10]byte)
			defer commandBuffers.Put(pooled)
			buffer := pooled[:]
			for {
				n, err := r.Read(buffer)
				if n > 0 {
					tails[i].add(buffer[:n])
					sinkMu.Lock()
					if sinkErr == nil {
						sinkErr = sink(stream, buffer[:n], false)
					}
					failed := sinkErr != nil
					sinkMu.Unlock()
					if failed {
						// safety: a dead sink must not leave the command blocked on a full pipe.
						session.kill()
					}
				}
				if err != nil {
					break
				}
			}
			sinkMu.Lock()
			if sinkErr == nil {
				sinkErr = sink(stream, nil, true)
			}
			sinkMu.Unlock()
		}()
	}
	waitErr := cmd.Wait()
	close(exited)
	// safety: a background child holding the pipes ends the drain at the deadline.
	deadline := time.Now().Add(commandDrainGrace)
	if outR.SetReadDeadline(deadline) != nil || errR.SetReadDeadline(deadline) != nil {
		timer := time.AfterFunc(commandDrainGrace, func() { closeFiles(outR, errR) })
		defer timer.Stop()
	}
	drains.Wait()
	closeFiles(outR, errR)
	var exit *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exit) && !errors.Is(waitErr, exec.ErrWaitDelay) {
		done.StartError = waitErr.Error()
		return done, session
	}
	done.ExitCode = cmd.ProcessState.ExitCode()
	done.Signaled = !cmd.ProcessState.Exited()
	done.Stdout, done.Stderr = tails[0].bytes(), tails[1].bytes()
	return done, session
}

type commandTail struct {
	keep bool
	data []byte
}

func (t *commandTail) add(chunk []byte) {
	if !t.keep {
		return
	}
	t.data = append(t.data, chunk...)
	if len(t.data) > 2*nodeproto.ExecTailBytes {
		t.data = append(t.data[:0], t.data[len(t.data)-nodeproto.ExecTailBytes:]...)
	}
}

func (t *commandTail) bytes() []byte {
	if len(t.data) > nodeproto.ExecTailBytes {
		return t.data[len(t.data)-nodeproto.ExecTailBytes:]
	}
	return t.data
}

// lookCommand searches as Node's spawn does, since a Go SDK sends the path it resolved
// itself: the command's own PATH, relative and empty entries against its directory, and
// the C library's default when PATH is unset.
func lookCommand(name string, env []string, dir string) (string, error) {
	if strings.Contains(name, string(filepath.Separator)) {
		return name, nil
	}
	path, set := "", false
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path, set = value, true
		}
	}
	if !set {
		path = defaultSearchPath
	}
	for _, entry := range filepath.SplitList(path) {
		if entry == "" {
			entry = "."
		}
		candidate := filepath.Join(entry, name)
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(dir, candidate)
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// defaultSearchPath is glibc's _CS_PATH, which execvp uses when PATH is unset.
const defaultSearchPath = "/bin:/usr/bin"

func closeFiles(files ...io.Closer) {
	for _, f := range files {
		_ = f.Close()
	}
}
