// SPDX-License-Identifier: AGPL-3.0-or-later

// Package dllsafe hardens the DLL search order of the Windows programs
// (TuxMail.exe, TuxMail-Setup.exe) against DLL planting (AUDIT-D D-02).
//
// Several dependencies (Wails, winc) load DLLs with syscall.NewLazyDLL,
// i.e. LoadLibrary with the default search order, which starts with the
// directory of the executable. dwmapi.dll, shcore.dll and gdiplus.dll are
// not KnownDLLs, so a DLL of that name next to a setup
// program in the Downloads folder would be loaded, in the elevated child
// process as well. The init function of this package restricts the default
// search path to System32 (SetDefaultDllDirectories) before any of those
// DLLs is loaded.
//
// Import it for its side effect as early as possible:
//
//	import _ "github.com/Midas1989/Mailclient/internal/dllsafe"
//
// It depends on nothing but the standard library, so the Go runtime
// initializes it before the GUI packages (package initialization order:
// dependencies first, otherwise by import path).
package dllsafe
