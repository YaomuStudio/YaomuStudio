//go:build windows

package execadapter

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Job handle belongs only to Core. Children inherit membership, never the
// handle; Core death closes the last handle and terminates every job member.
//
// Job membership covers Windows processes only. When Core launches wsl.exe, the
// Linux execution tree inside WSL2 is outside the Job and survives
// TerminateJobObject; coversDetachedChildren records that limit honestly.
type processTree struct {
	job                    windows.Handle
	coversDetachedChildren bool
	closeOnce              sync.Once
	closeErr               error
}

func configureProcessGroup(cmd *exec.Cmd) {}

func attachProcessTree(cmd *exec.Cmd, uncovered bool) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create runner job: %w", err)
	}
	fail := func(cause error) (*processTree, error) {
		return nil, errors.Join(cause, windows.CloseHandle(job))
	}
	if err := windows.SetHandleInformation(job, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return fail(fmt.Errorf("make runner job handle non-inheritable: %w", err))
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fail(fmt.Errorf("configure runner job: %w", err))
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(fmt.Errorf("open owned runner for job assignment: %w", err))
	}
	assignErr := windows.AssignProcessToJobObject(job, process)
	closeErr := windows.CloseHandle(process)
	if err := errors.Join(assignErr, closeErr); err != nil {
		return fail(fmt.Errorf("assign runner to job: %w", err))
	}
	return &processTree{
		job:                    job,
		coversDetachedChildren: !uncovered && commandCoversDetachedChildren(processTreeLaunchCommand(cmd)),
	}, nil
}

func (t *processTree) Kill(bool) error {
	if err := windows.TerminateJobObject(t.job, 1); err != nil {
		return fmt.Errorf("terminate runner job: %w", err)
	}
	return nil
}

// JOB_OBJECT_BASIC_ACCOUNTING_INFORMATION is fixed-width on both Windows ABIs.
type jobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func (t *processTree) Exited() (bool, error) {
	info := jobAccounting{}
	if err := windows.QueryInformationJobObject(t.job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return false, fmt.Errorf("query runner job exit: %w", err)
	}
	return info.ActiveProcesses == 0, nil
}

func (t *processTree) Close() error {
	t.closeOnce.Do(func() { t.closeErr = windows.CloseHandle(t.job) })
	return t.closeErr
}

func (t *processTree) CoversDetachedChildren() bool { return t.coversDetachedChildren }
