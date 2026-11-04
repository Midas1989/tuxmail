// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build windows

package winsetup

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unicode/utf16"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

// ShellLinker creates .lnk files through the Windows Script Host shell
// object (a thin automation wrapper around IShellLink); if COM fails it
// falls back to PowerShell.
type ShellLinker struct{}

var _ Linker = ShellLinker{}

// CreateShortcut implements Linker.
func (ShellLinker) CreateShortcut(link string, s Shortcut) error {
	errc := make(chan error, 1)
	go func() {
		// COM objects are bound to the thread that created them.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		errc <- comShortcut(link, s)
	}()
	err := <-errc
	if err == nil {
		return nil
	}
	if perr := powershellShortcut(link, s); perr != nil {
		return fmt.Errorf("%v; PowerShell fallback: %v", err, perr)
	}
	return nil
}

func comShortcut(link string, s Shortcut) error {
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		var oe *ole.OleError
		if !errors.As(err, &oe) || oe.Code() != 1 { // S_FALSE: already initialized
			return err
		}
	}
	defer ole.CoUninitialize()
	unk, err := oleutil.CreateObject("WScript.Shell")
	if err != nil {
		return err
	}
	defer unk.Release()
	shell, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer shell.Release()
	v, err := oleutil.CallMethod(shell, "CreateShortcut", link)
	if err != nil {
		return err
	}
	sc := v.ToIDispatch()
	defer sc.Release()
	for _, p := range [][2]string{{"TargetPath", s.Target}, {"Arguments", s.Args}, {"WorkingDirectory", s.WorkDir},
		{"IconLocation", s.Icon}, {"Description", s.Description}} {
		if p[1] == "" {
			continue
		}
		if _, err := oleutil.PutProperty(sc, p[0], p[1]); err != nil {
			return fmt.Errorf("%s: %w", p[0], err)
		}
	}
	_, err = oleutil.CallMethod(sc, "Save")
	return err
}

// powershellShortcut passes all values through the environment, so no
// quoting of user-chosen paths is needed.
func powershellShortcut(link string, s Shortcut) error {
	const script = `$ErrorActionPreference='Stop';` +
		`$s=(New-Object -ComObject WScript.Shell).CreateShortcut($env:TM_LNK);` +
		`$s.TargetPath=$env:TM_TARGET;if($env:TM_ARGS){$s.Arguments=$env:TM_ARGS};` +
		`$s.WorkingDirectory=$env:TM_DIR;if($env:TM_ICON){$s.IconLocation=$env:TM_ICON};` +
		`if($env:TM_DESC){$s.Description=$env:TM_DESC};$s.Save()`
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.Env = append(os.Environ(), "TM_LNK="+link, "TM_TARGET="+s.Target, "TM_ARGS="+s.Args,
		"TM_DIR="+s.WorkDir, "TM_ICON="+s.Icon, "TM_DESC="+s.Description)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// ShellFolders returns the start menu "Programs" and desktop folders of a
// scope (common folders for all users).
func ShellFolders(s Scope) (Folders, error) {
	sm, desk := windows.FOLDERID_Programs, windows.FOLDERID_Desktop
	if s == ScopeMachine {
		sm, desk = windows.FOLDERID_CommonPrograms, windows.FOLDERID_PublicDesktop
	}
	var f Folders
	var err error
	if f.StartMenu, err = windows.KnownFolderPath(sm, windows.KF_FLAG_CREATE); err != nil {
		return f, fmt.Errorf("start menu folder: %w", err)
	}
	if f.Desktop, err = windows.KnownFolderPath(desk, windows.KF_FLAG_DEFAULT); err != nil {
		return f, fmt.Errorf("desktop folder: %w", err)
	}
	return f, nil
}

var (
	shell32            = windows.NewLazySystemDLL("shell32.dll")
	procSHChangeNotify = shell32.NewProc("SHChangeNotify")
	procShellExecuteEx = shell32.NewProc("ShellExecuteExW")
)

// NotifyAssociationsChanged tells Explorer that file/URL associations and
// shortcuts changed (SHCNE_ASSOCCHANGED).
func NotifyAssociationsChanged() {
	const shcneAssocChanged, shcnfIDList = 0x08000000, 0
	_, _, _ = procSHChangeNotify.Call(shcneAssocChanged, shcnfIDList, 0, 0)
}

// OpenURL opens a URL or ms-settings: page with the default handler.
func OpenURL(u string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// OpenDefaultApps opens Settings > Apps > Default apps at TuxMail (Windows
// 11 understands registeredAppUser/registeredAppMachine; Windows 10 shows
// the overview).
func OpenDefaultApps(scope Scope) error {
	param := "registeredAppUser"
	if scope == ScopeMachine {
		param = "registeredAppMachine"
	}
	return OpenURL("ms-settings:defaultapps?" + param + "=" + AppName)
}
