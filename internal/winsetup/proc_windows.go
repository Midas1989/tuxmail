// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package winsetup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// RunningProcess is a TuxMail process (or plugin) started from an
// installation folder.
type RunningProcess struct {
	PID  uint32
	Path string
}

func processPath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

// FindRunning lists processes whose executable lives in dir (TuxMail.exe
// and the messenger plugins). Processes of other users are only visible to
// an elevated setup.
func FindRunning(dir string) []RunningProcess {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	self := uint32(os.Getpid())
	var out []RunningProcess
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		name := strings.ToLower(windows.UTF16ToString(pe.ExeFile[:]))
		if pe.ProcessID == self || (name != strings.ToLower(AppExe) && !strings.HasPrefix(name, "tuxmail-plugin-")) {
			continue
		}
		p := processPath(pe.ProcessID)
		if p != "" && WinWithin(dir, p) && !strings.EqualFold(filepath.Base(p), SetupExe) {
			out = append(out, RunningProcess{PID: pe.ProcessID, Path: p})
		}
	}
	return out
}

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	procPostMessage              = user32.NewProc("PostMessageW")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")
)

// CloseRunning asks TuxMail to quit (WM_CLOSE to its windows), waits up to
// timeout and terminates what is still running (plugins exit with it).
func CloseRunning(dir string, timeout time.Duration) error {
	procs := FindRunning(dir)
	if len(procs) == 0 {
		return nil
	}
	pids := map[uint32]bool{}
	for _, p := range procs {
		pids[p.PID] = true
	}
	const wmClose = 0x0010
	cb := syscall.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var pid uint32
		_, _, _ = procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pids[pid] {
			_, _, _ = procPostMessage.Call(hwnd, wmClose, 0, 0)
		}
		return 1
	})
	_, _, _ = procEnumWindows.Call(cb, 0)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(FindRunning(dir)) == 0 {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	var errs []error
	for _, p := range FindRunning(dir) {
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, p.PID)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s (%d): %w", filepath.Base(p.Path), p.PID, err))
			continue
		}
		if err := windows.TerminateProcess(h, 1); err != nil {
			errs = append(errs, err)
		}
		_, _ = windows.WaitForSingleObject(h, 5000)
		windows.CloseHandle(h)
	}
	return errors.Join(errs...)
}

// IsElevated reports whether the setup runs with administrator rights.
func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// ErrCancelled: the user declined the UAC prompt.
var ErrCancelled = errors.New("cancelled")

type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

// RunElevated starts exe with "runas" (UAC prompt) and returns the process
// handle; the caller waits for it with WaitProcess.
func RunElevated(exe string, args []string) (windows.Handle, error) {
	const seeMaskNoCloseProcess, seeMaskNoAsync = 0x00000040, 0x00000100
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	params, err := windows.UTF16PtrFromString(QuoteArgs(args))
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync, lpVerb: verb, lpFile: file,
		lpParameters: params, nShow: windows.SW_HIDE}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, callErr := procShellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, ErrCancelled
		}
		return 0, callErr
	}
	if info.hProcess == 0 {
		return 0, errors.New("no process handle from ShellExecuteEx")
	}
	return info.hProcess, nil
}

// WaitProcess waits for a process handle (polling, so that poll can read
// progress meanwhile) and returns its exit code.
func WaitProcess(h windows.Handle, poll func()) (uint32, error) {
	defer windows.CloseHandle(h)
	for {
		ev, err := windows.WaitForSingleObject(h, 150)
		if err != nil {
			return 0, err
		}
		if poll != nil {
			poll()
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
	}
	var code uint32
	err := windows.GetExitCodeProcess(h, &code)
	return code, err
}

// Launch starts TuxMail without waiting. An elevated setup starts it via
// Explorer so that TuxMail does not inherit administrator rights.
func Launch(exe string) error {
	if IsElevated() {
		return exec.Command(filepath.Join(os.Getenv("SystemRoot"), "explorer.exe"), exe).Start()
	}
	cmd := exec.Command(exe)
	cmd.Dir = filepath.Dir(exe)
	return cmd.Start()
}

// ScheduleDelete removes files and then (empty) folders a few seconds
// after the setup has exited, with a hidden cmd.exe (a running program
// cannot delete itself).
func ScheduleDelete(files []string, dirs []string) error {
	var b strings.Builder
	b.WriteString("ping -n 4 127.0.0.1 >nul")
	// Paths with '%' or '"' are skipped: cmd.exe would expand or unquote
	// them (AUDIT-D D-07); such leftovers stay on disk.
	for _, f := range files {
		if SafeForCmd(f) {
			fmt.Fprintf(&b, ` & del /f /q "%s"`, f)
		}
	}
	for _, d := range dirs {
		if SafeForCmd(d) {
			fmt.Fprintf(&b, ` & rmdir "%s"`, d)
		}
	}
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW,
		CmdLine: `cmd.exe /d /s /c "` + b.String() + `"`}
	cmd.Dir = os.TempDir()
	return cmd.Start()
}

// CopyToTemp copies the setup program to %TEMP% so that uninstall can
// delete the installation folder, and returns the copy's path.
func CopyToTemp(self string) (string, error) {
	// Elevated: a folder only administrators can write to (AUDIT-D D-03).
	dir, err := PrivateTempDir("TuxMail-Uninstall-")
	if err != nil {
		return "", err
	}
	dst := filepath.Join(dir, SetupExe)
	if _, err := copySelf(self, dst); err != nil {
		return "", err
	}
	return dst, nil
}
