package nodehost

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

type commandSession struct{ job *procgroup.Job }

// safety: distinguish host termination from a natural nonzero exit racing cancellation.
const commandCancellationExitCode uint32 = 0x53574341

// safety: host execution assumes Bash inherits Windows environment variables.
// Shared SDK helpers and cleanups must keep their original argv.
func configureHostCommand(cmd *exec.Cmd) {
	if cmd.Err != nil || !strings.EqualFold(filepath.Base(cmd.Path), "bash.exe") {
		return
	}
	path, err := filepath.Abs(cmd.Path)
	if err != nil {
		cmd.Err = fmt.Errorf("resolve Windows Bash program: %w", err)
		return
	}
	if strings.EqualFold(path, filepath.Join(os.Getenv("SystemRoot"), "System32", "bash.exe")) {
		cmd.Err = errors.New("WSL bash.exe is not supported for host commands; install Git Bash and set the Bash path to its bash.exe (for example C:\\Program Files\\Git\\bin\\bash.exe)")
		return
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "-c" {
		return
	}
	// bug: Git Bash truncates long -c arguments from Windows; the environment preserves the program and stdin.
	const variable = "SPARKWING_BASH_PROGRAM"
	const limit = 32767 - len(variable) - 2
	program := cmd.Args[2]
	if len(utf16.Encode([]rune(program))) > limit {
		cmd.Err = fmt.Errorf("windows Bash program exceeds environment limit of %d UTF-16 code units; write the program to a script file and invoke it with bash script.sh", limit)
		return
	}
	// safety: an empty program succeeds like an empty script on Linux; only a lost variable fails.
	cmd.Args[2] = `: "${` + variable + `?host Bash program is missing from the command environment}"; eval 'unset ` + variable + `;' "$` + variable + `"`
	cmd.Env = append(cmd.Environ(), variable+"="+program)
}

func execFormatError(error) bool { return false }

func startHostCommand(cmd *exec.Cmd, _ []string) (*commandSession, error) {
	job, err := procgroup.NewJob("")
	if err != nil {
		return nil, err
	}
	if err := job.Start(cmd); err != nil {
		closeErr := job.Close()
		if cmd.Process != nil {
			err = errors.Join(err, cmd.Wait())
		}
		return nil, errors.Join(err, closeErr)
	}
	return &commandSession{job: job}, nil
}

func (c *commandSession) leader() (int, string, bool) { return 0, "", false }

func (c *commandSession) kill() error {
	if err := c.job.TerminateWithCode(commandCancellationExitCode); err != nil {
		return fmt.Errorf("kill SDK command job: %w", err)
	}
	return nil
}

func (*commandSession) signaled(cmd *exec.Cmd) bool {
	return uint32(cmd.ProcessState.ExitCode()) == commandCancellationExitCode
}

func endCommandSessions(sessions []*commandSession) error {
	var errs []error
	for _, session := range sessions {
		errs = append(errs, session.job.Close())
	}
	return errors.Join(errs...)
}

// safety: overlapped reads allow drain deadlines without discarding output buffered behind a slow sink.
func commandPipe() (*os.File, *os.File, error) {
	name, err := windows.UTF16PtrFromString(`\\.\pipe\sparkwing-exec-` + rand.Text())
	if err != nil {
		return nil, nil, err
	}
	read, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 0, 64<<10, 0, nil)
	if err != nil {
		return nil, nil, err
	}
	write, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, nil, errors.Join(err, windows.CloseHandle(read))
	}
	return os.NewFile(uintptr(read), "exec-read"), os.NewFile(uintptr(write), "exec-write"), nil
}

var peekNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

func bufferedBytes(f *os.File) int {
	conn, err := f.SyscallConn()
	if err != nil {
		return 0
	}
	var available uint32
	if err := conn.Control(func(fd uintptr) {
		if ok, _, callErr := peekNamedPipe.Call(fd, 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0); ok == 0 && callErr != nil {
			available = 0
		}
	}); err != nil {
		return 0
	}
	return int(available)
}
