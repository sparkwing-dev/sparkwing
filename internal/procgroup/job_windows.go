//go:build windows

package procgroup

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procOpenJobObject = kernel32.NewProc("OpenJobObjectW")
)

// ProcessBirth returns the token that distinguishes this incarnation of pid
// from any later process that reuses the number: its creation time.
func ProcessBirth(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return "", fmt.Errorf("%w: process %d", ErrProcessAbsent, pid)
		}
		return "", err
	}
	defer windows.CloseHandle(handle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", err
	}
	return strconv.FormatInt(creation.Nanoseconds(), 10), nil
}

// Job is a kill-on-close Job Object holding one step command and everything
// it spawns. Closing it ends every member; a named job can also be opened
// and terminated by another process while a handle is still open.
type Job struct {
	Name   string
	handle windows.Handle
}

// NewJob creates a named, kill-on-close job with no members yet.
func NewJob(name string) (*Job, error) {
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	job, err := windows.CreateJobObject(nil, wide)
	if err != nil {
		return nil, fmt.Errorf("create step job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("protect step job: %w", err)
	}
	return &Job{Name: name, handle: job}, nil
}

// Start starts cmd suspended inside the job and resumes it, so nothing the
// command spawns exists outside the job.
func (j *Job) Start(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// safety: suspension closes the window in which the command could spawn
	// before it is assigned to the job.
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := cmd.Start(); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("open suspended step: %w", err)
	}
	defer windows.CloseHandle(process)
	threads, err := suspendedThreads(uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	defer closeHandles(threads)
	if err := windows.AssignProcessToJobObject(j.handle, process); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("assign step to job: %w", err)
	}
	for _, thread := range threads {
		if _, err := windows.ResumeThread(thread); err != nil {
			_ = windows.TerminateJobObject(j.handle, 1)
			return fmt.Errorf("resume step: %w", err)
		}
	}
	return nil
}

// hack: x/sys does not expose the Win32 JOBOBJECT_BASIC_ACCOUNTING_INFORMATION layout.
type jobBasicAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

// ActiveProcesses counts the members still running.
func (j *Job) ActiveProcesses() (uint32, error) {
	var info jobBasicAccounting
	if err := windows.QueryInformationJobObject(j.handle, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return 0, err
	}
	return info.ActiveProcesses, nil
}

// Terminate ends every member of the job now.
func (j *Job) Terminate() error {
	if j == nil {
		return nil
	}
	return windows.TerminateJobObject(j.handle, 1)
}

// Close releases the handle; kill-on-close ends any member still running.
func (j *Job) Close() error {
	if j == nil {
		return nil
	}
	return windows.CloseHandle(j.handle)
}

// TerminateJobByName ends every member of the named job from any process.
// A name nobody holds any more means the job, and its members, are gone.
func TerminateJobByName(name string) error {
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	const jobObjectTerminate = 0x0008
	handle, _, callErr := procOpenJobObject.Call(uintptr(jobObjectTerminate), 0, uintptr(unsafe.Pointer(wide)))
	if handle == 0 {
		if errors.Is(callErr, windows.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		return fmt.Errorf("open job %q: %w", name, callErr)
	}
	job := windows.Handle(handle)
	defer windows.CloseHandle(job)
	return windows.TerminateJobObject(job, 1)
}

func suspendedThreads(pid uint32) ([]windows.Handle, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return nil, fmt.Errorf("list step threads: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("read step threads: %w", err)
	}
	var threads []windows.Handle
	for {
		if entry.OwnerProcessID == pid {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				closeHandles(threads)
				return nil, fmt.Errorf("open step thread: %w", err)
			}
			threads = append(threads, thread)
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		err = windows.Thread32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			closeHandles(threads)
			return nil, fmt.Errorf("read step threads: %w", err)
		}
	}
	if len(threads) == 0 {
		return nil, errors.New("suspended step has no thread to resume")
	}
	return threads, nil
}

func closeHandles(handles []windows.Handle) {
	for _, h := range handles {
		_ = windows.CloseHandle(h)
	}
}
