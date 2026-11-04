; SPDX-License-Identifier: AGPL-3.0-or-later
; Copyright (C) 2026 Midas Wollinger and TuxMail contributors
;
; Classic NSIS installer for TuxMail (Windows desktop), built by
; build/windows/package-installer.sh. The primary installer is the TuxMail
; setup program (cmd/tuxmail-setup, build/windows/package-setup.sh); this one
; is kept as fallback artifact "...-setup-classic.exe".
;   makensis -DVERSION=0.1.0 -DVERSION_NUMERIC=0.1.0.0 -DARCH=amd64 -DEXE=...\TuxMail.exe
;            [-DPLUGINS=...\plugins] -DROOT=<repo> -DOUTFILE=...\TuxMail-0.1.0-windows-amd64-setup-classic.exe
;            [-DWEBVIEW2_BOOTSTRAPPER=...\MicrosoftEdgeWebview2Setup.exe] installer.nsi
;
; - Default: installation for the current user only, no administrator rights
;   (%LOCALAPPDATA%\Programs\TuxMail, HKCU). Optional: all users
;   (%ProgramFiles%\TuxMail, HKLM) - the installer restarts itself elevated (UAC).
;   Silent: setup.exe /S [/ALLUSERS | /CURRENTUSER] [/D=C:\Path]
; - Start menu shortcut, optional desktop shortcut, uninstaller + "Apps" entry
; - Optional: register TuxMail as mail program (mailto:) - off by default
;   (KONZEPT 15.2: system integration only on request)
; - Checks for the Microsoft Edge WebView2 runtime and installs it if missing
;   (bundled bootstrapper, else link to the download page)
; Requires NSIS 3.04+ (tested with 3.09/3.10). Only standard plugins are used.

Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma
RequestExecutionLevel user

!ifndef VERSION
  !error "VERSION not defined"
!endif
!ifndef VERSION_NUMERIC
  !define VERSION_NUMERIC "0.0.0.0"
!endif
!ifndef ARCH
  !define ARCH "amd64"
!endif
!ifndef ROOT
  !define ROOT "..\.."
!endif
!ifndef EXE
  !define EXE "${ROOT}\build\out\windows-${ARCH}\TuxMail.exe"
!endif
!ifndef OUTFILE
  !define OUTFILE "${ROOT}\dist\TuxMail-${VERSION}-windows-${ARCH}-setup-classic.exe"
!endif

!define APPNAME "TuxMail"
!define PUBLISHER "TuxMail"
!define APPKEY "TuxMail"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPKEY}"
!define WEBVIEW2_GUID "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"
!define WEBVIEW2_URL "https://go.microsoft.com/fwlink/p/?LinkId=2124703"
!define HOMEPAGE "https://github.com/Midas1989/Mailclient"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "nsDialogs.nsh"
!include "FileFunc.nsh"
!include "x64.nsh"
!include "WinVer.nsh"

Name "${APPNAME}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\${APPNAME}"
BrandingText "${APPNAME} ${VERSION}"
ShowInstDetails show
ShowUninstDetails show

VIProductVersion "${VERSION_NUMERIC}"
VIFileVersion "${VERSION_NUMERIC}"
VIAddVersionKey /LANG=0 "ProductName" "${APPNAME}"
VIAddVersionKey /LANG=0 "CompanyName" "${PUBLISHER}"
VIAddVersionKey /LANG=0 "LegalCopyright" "Copyright (C) 2026 Midas Wollinger and TuxMail contributors. License: AGPL-3.0-or-later"
VIAddVersionKey /LANG=0 "FileDescription" "${APPNAME} Setup"
VIAddVersionKey /LANG=0 "FileVersion" "${VERSION_NUMERIC}"
VIAddVersionKey /LANG=0 "ProductVersion" "${VERSION}"

Var AllUsers        ; 1 = all users (HKLM, Program Files), 0 = current user
Var IsAdmin
Var RbCurrent
Var RbAll

; ---------------------------------------------------------------- pages
!define MUI_ICON "${ROOT}\assets\branding\icons\tuxmail.ico"
!define MUI_UNICON "${ROOT}\assets\branding\icons\tuxmail.ico"
!define MUI_ABORTWARNING
!define MUI_COMPONENTSPAGE_NODESC

!insertmacro MUI_PAGE_WELCOME
; The AGPL does not require acceptance for installation and use.
!define MUI_LICENSEPAGE_BUTTON "$(^NextBtn)"
!define MUI_LICENSEPAGE_TEXT_BOTTOM "$(LicenseBottom)"
!insertmacro MUI_PAGE_LICENSE "${ROOT}\LICENSE"
Page custom InstallModePage InstallModeLeave
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\TuxMail.exe"
!define MUI_FINISHPAGE_LINK "${HOMEPAGE}"
!define MUI_FINISHPAGE_LINK_LOCATION "${HOMEPAGE}"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

; The UI language follows the Windows display language (English is the default).
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "German"

LangString LicenseBottom ${LANG_ENGLISH} "TuxMail is free software (GNU AGPL v3 or later). You do not need to accept this license to install or use TuxMail."
LangString LicenseBottom ${LANG_GERMAN} "TuxMail ist freie Software (GNU AGPL v3 oder später). Für Installation und Nutzung müssen Sie dieser Lizenz nicht zustimmen."
LangString ModeTitle ${LANG_ENGLISH} "Installation type"
LangString ModeTitle ${LANG_GERMAN} "Installationsart"
LangString ModeSubtitle ${LANG_ENGLISH} "Choose for whom TuxMail is installed."
LangString ModeSubtitle ${LANG_GERMAN} "Wählen Sie, für wen TuxMail installiert wird."
LangString ModeCurrent ${LANG_ENGLISH} "Only for me (recommended, no administrator rights needed)"
LangString ModeCurrent ${LANG_GERMAN} "Nur für mich (empfohlen, keine Administratorrechte nötig)"
LangString ModeAll ${LANG_ENGLISH} "For all users of this computer (requires administrator rights)"
LangString ModeAll ${LANG_GERMAN} "Für alle Benutzer dieses Computers (erfordert Administratorrechte)"
LangString ModeElevate ${LANG_ENGLISH} "Setup will now restart with administrator rights."
LangString ModeElevate ${LANG_GERMAN} "Das Setup wird jetzt mit Administratorrechten neu gestartet."
LangString ElevateFailed ${LANG_ENGLISH} "Administrator rights are required for an installation for all users."
LangString ElevateFailed ${LANG_GERMAN} "Für eine Installation für alle Benutzer sind Administratorrechte erforderlich."
LangString WrongArch ${LANG_ENGLISH} "This installer is for Windows on ${ARCH}. Please download the matching TuxMail setup for this computer."
LangString WrongArch ${LANG_GERMAN} "Dieses Setup ist für Windows auf ${ARCH}. Bitte laden Sie das passende TuxMail-Setup für diesen Computer herunter."
LangString OldWindows ${LANG_ENGLISH} "TuxMail requires Windows 10 or later."
LangString OldWindows ${LANG_GERMAN} "TuxMail benötigt Windows 10 oder neuer."
LangString WebView2Missing ${LANG_ENGLISH} "TuxMail needs the Microsoft Edge WebView2 Runtime, which is not installed. Open the download page now?"
LangString WebView2Missing ${LANG_GERMAN} "TuxMail benötigt die Microsoft Edge WebView2 Runtime, die nicht installiert ist. Download-Seite jetzt öffnen?"
LangString WebView2Installing ${LANG_ENGLISH} "Installing Microsoft Edge WebView2 Runtime..."
LangString WebView2Installing ${LANG_GERMAN} "Microsoft Edge WebView2 Runtime wird installiert..."
LangString AppRunning ${LANG_ENGLISH} "Please close TuxMail before continuing."
LangString AppRunning ${LANG_GERMAN} "Bitte beenden Sie TuxMail, bevor Sie fortfahren."
LangString SecApp ${LANG_ENGLISH} "TuxMail (required)"
LangString SecApp ${LANG_GERMAN} "TuxMail (erforderlich)"
LangString SecDesktop ${LANG_ENGLISH} "Desktop shortcut"
LangString SecDesktop ${LANG_GERMAN} "Verknüpfung auf dem Desktop"
LangString SecMailto ${LANG_ENGLISH} "Register as e-mail program (mailto: links)"
LangString SecMailto ${LANG_GERMAN} "Als E-Mail-Programm registrieren (mailto:-Links)"
LangString AppDescription ${LANG_ENGLISH} "Email, calendar, contacts and messengers in one app"
LangString AppDescription ${LANG_GERMAN} "E-Mail, Kalender, Kontakte und Messenger in einer Anwendung"

; ---------------------------------------------------------------- helpers
!macro SetMode mode
  StrCpy $AllUsers ${mode}
  ${If} $AllUsers == 1
    SetShellVarContext all
    StrCpy $INSTDIR "$PROGRAMFILES64\${APPNAME}"
  ${Else}
    SetShellVarContext current
    StrCpy $INSTDIR "$LOCALAPPDATA\Programs\${APPNAME}"
  ${EndIf}
!macroend

Function CheckAdmin
  UserInfo::GetAccountType
  Pop $0
  ${If} $0 == "Admin"
    StrCpy $IsAdmin 1
  ${Else}
    StrCpy $IsAdmin 0
  ${EndIf}
FunctionEnd

; Restarts this installer elevated with /ALLUSERS and quits.
Function Elevate
  ${GetParameters} $0
  ${If} ${Silent}
    StrCpy $0 "$0 /ALLUSERS"
  ${Else}
    StrCpy $0 "/ALLUSERS"
  ${EndIf}
  ClearErrors
  ExecShell "runas" "$EXEPATH" "$0"
  ${If} ${Errors}
    MessageBox MB_ICONSTOP|MB_OK "$(ElevateFailed)" /SD IDOK
    SetErrorLevel 740
  ${EndIf}
  Quit
FunctionEnd

Function .onInit
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_ICONSTOP|MB_OK "$(OldWindows)" /SD IDOK
    Abort
  ${EndIf}
  !if "${ARCH}" == "arm64"
    ${IfNot} ${IsNativeARM64}
      MessageBox MB_ICONSTOP|MB_OK "$(WrongArch)" /SD IDOK
      Abort
    ${EndIf}
  !else
    ; amd64 builds also run on Windows on ARM (x64 emulation).
    ${IfNot} ${RunningX64}
      MessageBox MB_ICONSTOP|MB_OK "$(WrongArch)" /SD IDOK
      Abort
    ${EndIf}
  !endif

  Call CheckAdmin
  ; Default: current user; keep the mode of an existing installation.
  StrCpy $0 0
  SetRegView 64
  ReadRegStr $1 HKLM "${UNINSTKEY}" "InstallLocation"
  ReadRegStr $2 HKCU "${UNINSTKEY}" "InstallLocation"
  ${If} $1 != ""
  ${AndIf} $2 == ""
    StrCpy $0 1
  ${EndIf}
  ${GetParameters} $3
  ClearErrors
  ${GetOptions} $3 "/ALLUSERS" $4
  ${IfNot} ${Errors}
    StrCpy $0 1
  ${EndIf}
  ClearErrors
  ${GetOptions} $3 "/CURRENTUSER" $4
  ${IfNot} ${Errors}
    StrCpy $0 0
  ${EndIf}
  !insertmacro SetMode $0
  ; Upgrade: reuse the previous folder of the selected mode.
  ${If} $AllUsers == 1
  ${AndIf} $1 != ""
    StrCpy $INSTDIR $1
  ${ElseIf} $AllUsers == 0
  ${AndIf} $2 != ""
    StrCpy $INSTDIR $2
  ${EndIf}

  ${If} ${Silent}
  ${AndIf} $AllUsers == 1
  ${AndIf} $IsAdmin == 0
    Call Elevate
  ${EndIf}
FunctionEnd

Function InstallModePage
  ; Started elevated with /ALLUSERS (or chosen before): no question.
  ${If} $AllUsers == 1
  ${AndIf} $IsAdmin == 1
    Abort
  ${EndIf}
  !insertmacro MUI_HEADER_TEXT "$(ModeTitle)" "$(ModeSubtitle)"
  nsDialogs::Create 1018
  Pop $0
  ${NSD_CreateRadioButton} 0 10u 100% 14u "$(ModeCurrent)"
  Pop $RbCurrent
  ${NSD_CreateRadioButton} 0 32u 100% 14u "$(ModeAll)"
  Pop $RbAll
  ${If} $AllUsers == 1
    ${NSD_Check} $RbAll
  ${Else}
    ${NSD_Check} $RbCurrent
  ${EndIf}
  nsDialogs::Show
FunctionEnd

Function InstallModeLeave
  ${NSD_GetState} $RbAll $0
  ${If} $0 == ${BST_CHECKED}
    ${If} $IsAdmin == 0
      MessageBox MB_ICONINFORMATION|MB_OKCANCEL "$(ModeElevate)" IDOK +2
      Abort
      Call Elevate
    ${EndIf}
    !insertmacro SetMode 1
  ${Else}
    !insertmacro SetMode 0
  ${EndIf}
FunctionEnd

Function CheckWebView2
  ; WebView2 Evergreen runtime: per-machine (32-bit registry view) or per-user.
  SetRegView 32
  ReadRegStr $0 HKLM "SOFTWARE\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${EndIf}
  SetRegView 64
  ${If} $0 != ""
  ${AndIf} $0 != "0.0.0.0"
    DetailPrint "WebView2 Runtime $0"
    Return
  ${EndIf}
  !ifdef WEBVIEW2_BOOTSTRAPPER
    DetailPrint "$(WebView2Installing)"
    SetOutPath "$PLUGINSDIR"
    File "/oname=MicrosoftEdgeWebview2Setup.exe" "${WEBVIEW2_BOOTSTRAPPER}"
    ExecWait '"$PLUGINSDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install' $1
    DetailPrint "WebView2 bootstrapper exit code $1"
  !else
    MessageBox MB_ICONEXCLAMATION|MB_YESNO "$(WebView2Missing)" /SD IDNO IDNO +2
    ExecShell "open" "${WEBVIEW2_URL}"
  !endif
FunctionEnd

; ---------------------------------------------------------------- sections
Section "$(SecApp)" SecApp
  SectionIn RO
  SetRegView 64
  Call CheckWebView2

  SetOutPath "$INSTDIR"
  ; Retry while an old TuxMail.exe is still running.
  ClearErrors
  File "${EXE}"
  ${If} ${Errors}
    MessageBox MB_ICONEXCLAMATION|MB_RETRYCANCEL "$(AppRunning)" /SD IDCANCEL IDRETRY +2
    Abort
    File "${EXE}"
  ${EndIf}
  !ifdef PLUGINS
    ; Messenger plugins (plugins\ next to TuxMail.exe).
    SetOutPath "$INSTDIR\plugins"
    File "${PLUGINS}\tuxmail-plugin-*.exe"
    SetOutPath "$INSTDIR"
  !endif
  File "/oname=LICENSE.txt" "${ROOT}\LICENSE"
  File "/oname=NOTICE.txt" "${ROOT}\NOTICE"
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  CreateShortcut "$SMPROGRAMS\${APPNAME}.lnk" "$INSTDIR\TuxMail.exe" "" "$INSTDIR\TuxMail.exe" 0

  WriteRegStr SHCTX "${UNINSTKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr SHCTX "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr SHCTX "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\TuxMail.exe,0"
  WriteRegStr SHCTX "${UNINSTKEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr SHCTX "${UNINSTKEY}" "URLInfoAbout" "${HOMEPAGE}"
  WriteRegStr SHCTX "${UNINSTKEY}" "HelpLink" "${HOMEPAGE}/issues"
  WriteRegStr SHCTX "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr SHCTX "${UNINSTKEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegStr SHCTX "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegDWORD SHCTX "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD SHCTX "${UNINSTKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD SHCTX "${UNINSTKEY}" "EstimatedSize" "$0"
SectionEnd

Section /o "$(SecDesktop)" SecDesktop
  CreateShortcut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\TuxMail.exe" "" "$INSTDIR\TuxMail.exe" 0
SectionEnd

Section /o "$(SecMailto)" SecMailto
  SetRegView 64
  ; ProgID. The URL ("%1") is not passed yet because the command line has no
  ; "open mailto:" option; once it exists, use '"$INSTDIR\TuxMail.exe" "%1"'.
  WriteRegStr SHCTX "Software\Classes\TuxMail.mailto" "" "TuxMail mailto"
  WriteRegStr SHCTX "Software\Classes\TuxMail.mailto\DefaultIcon" "" "$INSTDIR\TuxMail.exe,0"
  WriteRegStr SHCTX "Software\Classes\TuxMail.mailto\shell\open\command" "" '"$INSTDIR\TuxMail.exe"'
  ; Mail client registration + capabilities (Settings > Apps > Default apps).
  WriteRegStr SHCTX "Software\Clients\Mail\TuxMail" "" "${APPNAME}"
  WriteRegStr SHCTX "Software\Clients\Mail\TuxMail\shell\open\command" "" '"$INSTDIR\TuxMail.exe"'
  WriteRegStr SHCTX "Software\Clients\Mail\TuxMail\Capabilities" "ApplicationName" "${APPNAME}"
  WriteRegStr SHCTX "Software\Clients\Mail\TuxMail\Capabilities" "ApplicationDescription" "$(AppDescription)"
  WriteRegStr SHCTX "Software\Clients\Mail\TuxMail\Capabilities" "ApplicationIcon" "$INSTDIR\TuxMail.exe,0"
  WriteRegStr SHCTX "Software\Clients\Mail\TuxMail\Capabilities\URLAssociations" "mailto" "TuxMail.mailto"
  WriteRegStr SHCTX "Software\RegisteredApplications" "TuxMail" "Software\Clients\Mail\TuxMail\Capabilities"
  WriteRegDWORD SHCTX "${UNINSTKEY}" "MailtoRegistered" 1
  ; Windows lets only the user pick the default program: open "Default apps".
  ${IfNot} ${Silent}
    ExecShell "open" "ms-settings:defaultapps"
  ${EndIf}
SectionEnd

; ---------------------------------------------------------------- uninstaller
Function un.onInit
  SetRegView 64
  StrCpy $AllUsers 0
  ReadRegStr $0 HKLM "${UNINSTKEY}" "InstallLocation"
  ${If} $0 == $INSTDIR
    StrCpy $AllUsers 1
  ${EndIf}
  ${If} $AllUsers == 1
    SetShellVarContext all
    UserInfo::GetAccountType
    Pop $1
    ${If} $1 != "Admin"
      ; Restart the (temporary copy of the) uninstaller elevated, in place.
      StrCpy $2 "_?=$INSTDIR"
      ${If} ${Silent}
        StrCpy $2 "/S $2"
      ${EndIf}
      ExecShell "runas" "$EXEPATH" "$2"
      Quit
    ${EndIf}
  ${Else}
    SetShellVarContext current
  ${EndIf}
FunctionEnd

Section "Uninstall"
  SetRegView 64
  Delete "$SMPROGRAMS\${APPNAME}.lnk"
  Delete "$DESKTOP\${APPNAME}.lnk"
  Delete "$INSTDIR\TuxMail.exe"
  Delete "$INSTDIR\LICENSE.txt"
  Delete "$INSTDIR\NOTICE.txt"
  Delete "$INSTDIR\Uninstall.exe"
  Delete "$INSTDIR\plugins\tuxmail-plugin-*.exe"
  RMDir "$INSTDIR\plugins"
  RMDir "$INSTDIR"

  DeleteRegKey SHCTX "Software\Classes\TuxMail.mailto"
  DeleteRegKey SHCTX "Software\Clients\Mail\TuxMail"
  DeleteRegValue SHCTX "Software\RegisteredApplications" "TuxMail"
  DeleteRegKey SHCTX "${UNINSTKEY}"
  ; User data (%APPDATA%\TuxMail) and credentials are kept on purpose.
SectionEnd
