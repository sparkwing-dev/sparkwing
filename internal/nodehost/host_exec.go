package nodehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type hostCommand struct {
	kill     chan struct{}
	killOnce sync.Once
}

func (c *hostCommand) stop() { c.killOnce.Do(func() { close(c.kill) }) }

// safety: a background child holding the pipes cannot keep the command open past this.
const commandDrainGrace = 500 * time.Millisecond

// perf: the daemon samples every few seconds, so a command that ends sooner skips the registration call.
const commandMeasureDelay = time.Second

// safety: the frame reader must keep draining log frames while the command runs, so this never blocks it.
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
		var measure func(int, string)
		if s.measureCommand != nil {
			measure = func(pid int, birth string) {
				if err := s.measureCommand(s.executionCtx, pid, birth); err != nil {
					slog.Default().Debug("SDK command not measured", "err", err)
				}
			}
		}
		done, session := runHostCommand(req, s.cfg.output.masker, command.kill, s.done, sink, measure)
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

// safety: these lines share the request pipe with host requests; the SDK routes them by their leading key.
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

// safety: a normal exit returns the session alive; the end of the SDK session kills what it left running.
// measure, when set, adds a command still running after commandMeasureDelay to the job's machine measurement.
func runHostCommand(req nodeproto.ExecRequest, masker *secrets.Masker, kill, closed <-chan struct{}, sink func(stream string, chunk []byte, eof bool) error, measure func(pid int, birth string)) (nodeproto.ExecDone, *commandSession) {
	done := nodeproto.ExecDone{ID: req.ID, ExitCode: -1}
	if info, err := os.Stat(req.Dir); err != nil {
		done.StartError = fmt.Sprintf("dir %q: %v", req.Dir, err)
		return done, nil
	} else if !info.IsDir() {
		done.StartError = fmt.Sprintf("dir %q: not a directory", req.Dir)
		return done, nil
	}
	path, err := procenv.LookupCommand(req.Argv[0], req.Env, req.Dir)
	if err != nil {
		done.StartError, done.NotFound = err.Error(), errors.Is(err, exec.ErrNotFound)
		return done, nil
	}
	outR, outW, err := commandPipe()
	if err != nil {
		done.StartError = err.Error()
		return done, nil
	}
	errR, errW, err := commandPipe()
	if err != nil {
		closeFiles(outR, outW)
		done.StartError = err.Error()
		return done, nil
	}
	command := func(path, arg0 string, args []string) *exec.Cmd {
		cmd := procenv.SDKCommand(req.Env, path, args...)
		cmd.Args[0], cmd.Dir, cmd.Stdout, cmd.Stderr = arg0, req.Dir, outW, errW
		if req.Stdin != nil {
			cmd.Stdin = bytes.NewReader(req.Stdin)
		}
		// safety: a background child holding stdin would otherwise keep Wait's copier, and the session, alive.
		cmd.WaitDelay = commandDrainGrace
		configureHostCommand(cmd)
		return cmd
	}
	cmd := command(path, req.Argv[0], req.Argv[1:])
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
	session, err := startHostCommand(cmd, req.Env)
	if err != nil && execFormatError(err) {
		// hack: execvp runs executable text without #! through /bin/sh for both SDKs.
		cmd = command("/bin/sh", "/bin/sh", append([]string{path}, req.Argv[1:]...))
		session, err = startHostCommand(cmd, req.Env)
	}
	if err != nil {
		closeFiles(outR, outW, errR, errW)
		done.StartError = err.Error()
		return done, nil
	}
	// safety: the child owns the write ends now; dropping ours lets the readers see EOF.
	closeFiles(outW, errW)
	stopMeasure := func() bool { return false }
	if pid, birth, ok := session.leader(); ok && measure != nil {
		stopMeasure = time.AfterFunc(commandMeasureDelay, func() { measure(pid, birth) }).Stop
	}
	exited := make(chan struct{})
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		select {
		case <-kill:
			session.kill()
		case <-closed:
			session.kill()
		case <-exited:
			select {
			case <-kill:
				session.kill()
			case <-closed:
				session.kill()
			default:
			}
		}
	}()
	tails := [2]*commandTail{{keep: !req.Capture, mask: masker.Stream()}, {keep: !req.Capture, mask: masker.Stream()}}
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
			owed := -1
			for {
				limit := len(buffer)
				if owed > 0 {
					limit = min(limit, owed)
				}
				n, err := r.Read(buffer[:limit])
				if owed > 0 {
					owed -= n
				}
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
				if owed == 0 {
					break
				}
				// safety: the deadline ends the wait for a lingering child, never bytes the exited command already wrote.
				if errors.Is(err, os.ErrDeadlineExceeded) && owed < 0 {
					if owed = bufferedBytes(r); owed > 0 && r.SetReadDeadline(time.Time{}) == nil {
						continue
					}
					break
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
	stopMeasure()
	close(exited)
	// safety: reaping the leader can finish before cancellation has killed its other process groups.
	<-cleanupDone
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
	done.Signaled = session.signaled(cmd)
	if done.Signaled {
		done.ExitCode = -1
	}
	done.Stdout, done.Stderr = tails[0].bytes(), tails[1].bytes()
	return done, session
}

type commandTail struct {
	keep bool
	mask *secrets.Stream
	data []byte
}

func (t *commandTail) add(chunk []byte) {
	if !t.keep {
		return
	}
	// safety: truncation must only discard bytes that have cleared whole-secret masking.
	if t.mask != nil {
		chunk = []byte(t.mask.Mask(string(chunk)))
	}
	t.data = append(t.data, chunk...)
	if len(t.data) > 2*nodeproto.ExecTailBytes {
		t.data = append(t.data[:0], t.data[len(t.data)-nodeproto.ExecTailBytes:]...)
	}
}

func (t *commandTail) bytes() []byte {
	if t.mask != nil {
		final := t.mask.Flush()
		t.mask = nil
		t.add([]byte(final))
	}
	if len(t.data) > nodeproto.ExecTailBytes {
		return t.data[len(t.data)-nodeproto.ExecTailBytes:]
	}
	return t.data
}

func closeFiles(files ...io.Closer) {
	for _, f := range files {
		_ = f.Close()
	}
}

// LocalCommands owns host-executed command sessions for an in-process runtime.
// The caller cancels and joins active Execute calls before Close.
type LocalCommands struct {
	mu       sync.Mutex
	masker   *secrets.Masker
	sessions []*commandSession
}

// NewLocalCommands binds local test execution to the host command implementation.
func NewLocalCommands(masker *secrets.Masker) *LocalCommands {
	if masker == nil {
		masker = secrets.NewMasker()
	}
	return &LocalCommands{masker: masker}
}

// Execute runs a command while retaining its background children until Close.
//
//nolint:contextcheck // Cancellation travels through the joined command channels.
func (c *LocalCommands) Execute(ctx context.Context, req nodeproto.ExecRequest, sink func(string, []byte, bool) error) nodeproto.ExecDone {
	done, session := runHostCommand(req, c.masker, ctx.Done(), ctx.Done(), sink, nil)
	if session != nil {
		c.mu.Lock()
		c.sessions = append(c.sessions, session)
		c.mu.Unlock()
	}
	return done
}

// Close joins the command sessions after their callers have returned.
func (c *LocalCommands) Close() error {
	c.mu.Lock()
	sessions := c.sessions
	c.sessions = nil
	c.mu.Unlock()
	return endCommandSessions(sessions)
}

// ExecuteCommand runs a command for an in-process SDK host and joins its process tree.
//
//nolint:contextcheck // Session cleanup bounds its own process-table read.
func ExecuteCommand(ctx context.Context, req nodeproto.ExecRequest, sink func(string, []byte, bool) error) nodeproto.ExecDone {
	commands := NewLocalCommands(nil)
	done := commands.Execute(ctx, req, sink)
	if err := commands.Close(); err != nil {
		done.StartError = err.Error()
	}
	return done
}
