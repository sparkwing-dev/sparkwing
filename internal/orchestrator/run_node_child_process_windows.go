//go:build windows

package orchestrator

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const assistedChildJobExitCode = 1

const assistedChildOwnershipBoundary = "job_object"

var resumeAssistedChildThread = windows.ResumeThread

type windowsAssistedChildProcess struct {
	cmd     *exec.Cmd
	job     windows.Handle
	done    chan struct{}
	waitErr error
}

// hack: x/sys does not expose the Win32 JOBOBJECT_BASIC_ACCOUNTING_INFORMATION layout.
type jobBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func startAssistedChildProcess(cmd *exec.Cmd, logger *slog.Logger) (assistedChildProcess, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create assisted child job: %w", err)
	}
	closeJob := true
	defer func() {
		if closeJob {
			_ = windows.CloseHandle(job)
		}
	}()

	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		return nil, fmt.Errorf("protect assisted child job: %w", err)
	}

	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// safety: suspension closes the window in which pipeline code could spawn before job assignment.
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		killUnownedSuspendedProcess(cmd)
		return nil, fmt.Errorf("open suspended assisted child: %w", err)
	}

	threads, err := suspendedProcessThreads(uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(process)
		killUnownedSuspendedProcess(cmd)
		return nil, err
	}
	defer closeWindowsHandles(threads)

	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(process)
		killUnownedSuspendedProcess(cmd)
		return nil, fmt.Errorf("assign suspended assisted child to job: %w", err)
	}
	_ = windows.CloseHandle(process)

	for _, thread := range threads {
		previous, resumeErr := resumeAssistedChildThread(thread)
		if resumeErr != nil || previous != 1 {
			settleFailedStartJob(job, cmd, logger)
			if resumeErr != nil {
				return nil, fmt.Errorf("resume assisted child: %w", resumeErr)
			}
			return nil, fmt.Errorf("resume assisted child: unexpected previous suspend count %d", previous)
		}
	}

	child := &windowsAssistedChildProcess{cmd: cmd, job: job, done: make(chan struct{})}
	closeJob = false
	go func() {
		child.waitErr = cmd.Wait()
		close(child.done)
	}()
	return child, nil
}

func suspendedProcessThreads(pid uint32) ([]windows.Handle, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return nil, fmt.Errorf("list suspended assisted child threads: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("read suspended assisted child threads: %w", err)
	}
	var threads []windows.Handle
	for {
		if entry.OwnerProcessID == pid {
			thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if openErr != nil {
				closeWindowsHandles(threads)
				return nil, fmt.Errorf("open suspended assisted child thread: %w", openErr)
			}
			threads = append(threads, thread)
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		err = windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			closeWindowsHandles(threads)
			return nil, fmt.Errorf("read suspended assisted child threads: %w", err)
		}
	}
	if len(threads) != 1 {
		closeWindowsHandles(threads)
		return nil, fmt.Errorf("suspended assisted child has unexpected thread count %d", len(threads))
	}
	return threads, nil
}

func closeWindowsHandles(handles []windows.Handle) {
	for _, handle := range handles {
		_ = windows.CloseHandle(handle)
	}
}

func killUnownedSuspendedProcess(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func (p *windowsAssistedChildProcess) exited() <-chan struct{} {
	return p.done
}

func (p *windowsAssistedChildProcess) finish(logger *slog.Logger) error {
	return p.settle(logger)
}

func (p *windowsAssistedChildProcess) terminate(logger *slog.Logger) error {
	return p.settle(logger)
}

func (p *windowsAssistedChildProcess) settle(logger *slog.Logger) error {
	// safety: returning on a job query failure could release helper capacity while the job remains active.
	retry := assistedChildCleanupRetryInitial
	terminated := false
	for {
		active, err := activeJobProcesses(p.job)
		if err == nil && active == 0 {
			break
		}
		if err == nil && !terminated {
			err = windows.TerminateJobObject(p.job, assistedChildJobExitCode)
			if err == nil {
				terminated = true
			}
		}
		if err != nil {
			logger.Error("assisted child job cleanup failed; retaining ownership",
				"process_id", p.cmd.Process.Pid, "err", err, "retry_in", retry)
		}
		time.Sleep(retry)
		retry = nextAssistedChildCleanupRetry(retry)
	}

	<-p.done
	_ = windows.CloseHandle(p.job)
	return p.waitErr
}

func activeJobProcesses(job windows.Handle) (uint32, error) {
	info := jobBasicAccountingInformation{}
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	return info.ActiveProcesses, err
}

func settleFailedStartJob(job windows.Handle, cmd *exec.Cmd, logger *slog.Logger) {
	failedAssistedChildCleanup{
		inspect: func() (bool, error) {
			active, err := activeJobProcesses(job)
			return active == 0, err
		},
		terminate: func() error {
			return windows.TerminateJobObject(job, assistedChildJobExitCode)
		},
		wait: func() {
			_ = cmd.Wait()
		},
		sleep:         time.Sleep,
		processID:     cmd.Process.Pid,
		boundary:      assistedChildOwnershipBoundary,
		inspectAction: "query_job",
		stopAction:    "terminate_job",
	}.settle(logger)
}
