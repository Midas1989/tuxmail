// SPDX-License-Identifier: AGPL-3.0-or-later

// Command tuxmail-setup is the Windows setup program of TuxMail
// (TuxMail-<version>-windows-<arch>-setup.exe): a frameless Wails/WebView2
// window in the TuxMail design that installs per user, for all users
// (elevated) or as portable folder, updates and uninstalls TuxMail.
//
// The application files (TuxMail.exe, plugins\, LICENSE.txt, NOTICE.txt)
// are embedded at build time from payload/payload.zip, written by
// build/windows/package-setup.sh; an optional
// payload/MicrosoftEdgeWebview2Setup.exe is used when WebView2 is missing.
// The setup logic lives in internal/winsetup; the user interface is the
// static page in ui/ (no build step; ui-dev/ has a browser mock for
// screenshots).
//
// Silent installation for administrators:
//
//	TuxMail-setup.exe /S [--scope=user|machine|portable] [--dir=PATH] [--desktop-shortcut] ...
//	TuxMail-Setup.exe --uninstall --silent [--remove-data]
//
// See winsetup.Usage and docs/RELEASE.md.
package main
