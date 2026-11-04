# Sicherheitsaudit D – Endgeräte, Installer, Pakete, Plugins

Umfang: Desktop-App (Wails), Windows-Setup (`cmd/tuxmail-setup`, `internal/winsetup`,
klassisches NSIS-Skript), Linux-Pakete (nfpm, AppImage, portable), Docker-Image,
systemd-Units, Messenger-Plugin-Host und Plugins, Android-App, PWA-Service-Worker,
Build-/Update-Pfade. Andere Bereiche (Server/Auth, Inhalte/XSS, Speicher/Krypto,
CI/dynamisch) prüfen die Audits A, B, C und E.

## Zusammenfassung (Deutsch)

Die Desktop-App ist grundsätzlich solide aufgebaut: Die WebView spricht die
Go-Seite nur über die Wails-Bindings an (Wails prüft den Ursprung jeder
IPC-Nachricht), der Asset-Server ist kein TCP-Listener, der Test-Hook ist nur mit
Loopback-Adresse **und** Token aktiv, OAuth nutzt PKCE, `state` und einen
kurzlebigen Loopback-Listener. Android exportiert nur die Launcher-Activity,
schließt Backups aus und verschlüsselt den Geräte-Token mit einem Keystore-Schlüssel
(AES-GCM, zufällige IV).

Gefunden und behoben wurden vor allem Probleme an **Vertrauensgrenzen zwischen
Prozessen und Benutzern**:

| ID | Schwere | Thema | Status |
|----|---------|-------|--------|
| D-01 | **Hoch** | Server-Modus: jeder Benutzer konnte `signal_cli_path` setzen, also ein beliebiges Programm auf dem Server starten lassen | behoben |
| D-02 | **Hoch** | DLL-Hijacking: Setup (meist im Download-Ordner) und TuxMail.exe laden `dwmapi.dll`, `shcore.dll` u. a. aus dem Programmordner, auch im erhöhten Kindprozess | behoben |
| D-03 | Mittel | Erhöhtes Setup führte den WebView2-Bootstrapper aus dem benutzerschreibbaren `%TEMP%` aus; Log mit festem Namen in `%TEMP%` | behoben |
| D-04 | Mittel | Deinstallation „für alle Benutzer“: die Kopie in `%TEMP%` wurde erst danach per UAC erhöht (TOCTOU) | behoben |
| D-05 | Mittel | Installation „für alle Benutzer“ außerhalb von Program Files (z. B. `C:\TuxMail`) erbte die Rechte von `C:\` (Benutzer dürfen ändern) | behoben |
| D-06 | Mittel | Plugin-Host suchte Plugins auch in `./bin/plugins` des Arbeitsverzeichnisses und prüfte weder Eigentümer noch Schreibrechte | behoben |
| D-07 | Niedrig | `ScheduleDelete`: `%VAR%` in Pfaden wird von cmd.exe expandiert | behoben |
| D-08 | Niedrig | Android-Teilen: URIs des eigenen FileProviders wurden angenommen | behoben |
| D-09 | Niedrig | Android: kein `FLAG_SECURE` bei aktiver App-Sperre (Vorschaubild im App-Umschalter) | behoben |
| D-10 | Niedrig | Android: keine explizite Network-Security-Config | behoben |
| D-11 | Niedrig | PWA: Benachrichtigungsklick öffnete beliebige URL aus dem Push | behoben |
| D-12 | Niedrig | AppImage: geerbtes `APPDIR` bestimmt das Arbeitsverzeichnis der WebKit-Hilfsprozesse | behoben |
| D-13 | Niedrig | Build: linuxdeploy/appimagetool als ungepinnte „continuous“-Downloads | optional pinbar |

Offene Restrisiken stehen am Ende (u. a. keine Signaturprüfung der Plugins,
unsignierte Windows-Binärdateien, Plugins laufen als derselbe Benutzer).

---

## English details

### Method

* Source review of everything listed in the scope above.
* Cross-builds: `GOOS=windows go build -tags desktop,production ./cmd/tuxmail` and
  `GOOS=windows go build ./cmd/tuxmail-setup`; PE inspection with `debug/pe`:
  both binaries import only `kernel32.dll` statically (`LoadLibraryExW`),
  ASLR (`DYNAMIC_BASE`, `HIGH_ENTROPY_VA`) and DEP (`NX_COMPAT`) are set. The
  setup manifest is `asInvoker` and elevates explicitly with `runas`.
* Dependency scan for `syscall.NewLazyDLL` / `windows.LoadDLL` across the
  Windows dependency graph (D-02) and a simulation of the Go package
  initialization order to verify that `internal/dllsafe` runs before any GUI
  package (only pure-computation packages such as `golang.org/x/text/...`
  initialize earlier).
* Unit tests with fake registry/filesystem (`internal/winsetup`), a plugin
  host run against malicious plugin directories (`internal/messenger/host`),
  a scripted AndroidManifest check (exported components, providers,
  `file://` filters, backup/cleartext flags), manual Kotlin review (Android
  Lint/Gradle are not available in the audit environment).
* Not possible here: running the Windows binaries (no Windows/Wine), building
  the Android app. Windows-only code is compiled and vetted (`GOOS=windows go vet`).

### D-01 (High) – server users choose the executable of the Signal plugin

`internal/messenger/plugins/signal` runs `env.Config["signal_cli_path"]`.
The value came from the per-user plugin settings
(`PATCH /api/messenger/plugins/signal {"settings":{"signal_cli_path":"..."}}`),
and in server mode every user gets their own plugin host on the server.
Any authenticated user could therefore make the server execute an arbitrary
program (with fixed arguments `--config <dir> --output=json jsonRpc ...`) as
the service account; stored settings even overrode the administrator's
`TUXMAIL_SIGNAL_SIGNAL_CLI_PATH`.

Fix: `Spec.HostSettings` marks settings that name host programs/paths
(`signal_cli_path`). With `messenger.Options.RestrictHostSettings` (set by
`internal/app` in `ModeServer`) such settings are rejected
(`ErrHostSetting` → HTTP 403), stored values are ignored by `pluginConfig`
(only the environment counts) and the keys are no longer offered in the
plugin list. Desktop mode is unchanged.
Regression tests: `internal/messenger/hostsettings_test.go`.

### D-02 (High) – DLL search order hijacking (setup in the Downloads folder)

Wails and its `winc` toolkit load `dwmapi.dll`, `shcore.dll`, `gdiplus.dll`,
`comctl32.dll` etc. with `syscall.NewLazyDLL`, i.e. `LoadLibrary`
with the default search order, whose first entry is the executable's own
folder. These DLLs are not KnownDLLs. A browser drive-by download of
`dwmapi.dll` into `Downloads` next to `TuxMail-Setup.exe` is loaded when the
setup starts – and again in the elevated child process the setup starts
from the same folder (all-users installation), which turns it into code
execution with administrator rights. The same applies to a portable
`TuxMail.exe` in a shared folder.

PoC (documented, Windows): place a DLL named `dwmapi.dll` exporting
`DwmSetWindowAttribute` next to the setup and start it; before the fix it
is loaded (Process Monitor: `CreateFile ...\Downloads\dwmapi.dll SUCCESS`).

Fix: new package `internal/dllsafe` calls
`SetDefaultDllDirectories(LOAD_LIBRARY_SEARCH_SYSTEM32)` in `init`. It is
imported by `cmd/tuxmail/main.go` and `cmd/tuxmail-setup/main_windows.go`
and depends only on `syscall`, so it is initialized before the GUI
packages. DLLs loaded with an absolute path (the WebView2 runtime's
`EmbeddedBrowserWebView.dll`) are unaffected. Regression test:
`internal/dllsafe/dllsafe_test.go` (import present, no non-std deps).
**To verify on Windows**: setup and desktop window still start (WebView2
runtime installed and missing).

### D-03 (Medium) – elevated setup uses the user's %TEMP%

The elevated (silent) setup wrote the embedded/downloaded WebView2
bootstrapper to `%TEMP%\MicrosoftEdgeWebview2Setup-<pid>.exe` and executed it.
`%TEMP%` belongs to the user and its ACL is inherited by files the
administrator creates there, so any non-elevated process of the user could
overwrite the file between write and `CreateProcess` and get administrator
rights without another UAC prompt. The elevated process also appended to the
fixed `%TEMP%\TuxMail-Setup.log` (link-following elevated write).

Fix: `winsetup.PrivateTempDir` creates, for an elevated setup, a new folder
with a random name and the DACL `O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)`
(`CreateDirectory` fails on an existing name, so it cannot be prepared
beforehand). Used for the WebView2 bootstrapper, the uninstaller copy (D-04)
and the elevated log when no `--log` is given.

### D-04 (Medium) – TOCTOU in the all-users uninstall

`TuxMail-Setup.exe --uninstall` in `C:\Program Files\TuxMail` copied itself
to `%TEMP%` (non-elevated) and the copy then requested elevation. Between the
copy and the UAC prompt another process of the user could replace the copy.

Fix: for `ScopeMachine` without elevation (`winsetup.ElevateBeforeTempCopy`)
the setup now elevates the copy in the protected installation folder first;
the elevated instance copies itself into a private temp folder (D-03) and
continues there. The scheduled clean-up also removes that folder.
Regression test: `TestElevateBeforeTempCopy`.

### D-05 (Medium) – weak ACL of all-users installations outside Program Files

An administrator may choose e.g. `C:\TuxMail` or `D:\Apps\TuxMail`. New
folders below `C:\` inherit `Authenticated Users: Modify`, so every user
could replace `TuxMail.exe` or drop a `plugins\tuxmail-plugin-*.exe` that
other users (and administrators) then run; the elevated uninstaller would
also follow a junction a user planted in the folder.

Fix: `Installer.Protect` (`winsetup.ProtectInstallDir` on Windows) applies
`O:BAD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;0x1200a9;;;BU)(A;OICI;0x1200a9;;;AC)`
(protected, owner Administrators) right after creating the folder and before
any file is written, for `ScopeMachine` outside the Program Files folders
(`NeedsProtectedACL`). If that fails, the fresh installation is rolled back.
Regression tests: `TestNeedsProtectedACL`,
`TestInstallProtectsMachineDirOutsideProgramFiles`.

### D-06 (Medium) – plugin discovery from the working directory, no trust check

`host.DefaultDirs` always included `<cwd>/bin/plugins`. Any TuxMail started
from a cloned repository, an extracted archive or – in server mode – its
`WorkingDirectory` (`/var/lib/tuxmail`, writable by the service account)
executed `bin/plugins/tuxmail-plugin-*` found there; a file-write bug in the
data directory became code execution. Plugins in world-writable folders or
owned by other users were also accepted.

Fix: the working directory is only searched by `go run`/`go test` binaries
(`isGoRunBinary`); developers can still set `TUXMAIL_PLUGIN_DIR`. On Unix
`checkTrusted` refuses plugin files and directories that are owned by a user
other than root or the current user, are world-writable (directories: unless
sticky) or group-writable by a foreign group (the user's own primary group is
allowed for umask 002 systems). Regression tests: `trust_test.go`
(malicious world-writable plugin dir, loose file mode, foreign owner, sticky
directory, working directory not searched).

Plugin host review otherwise: JSON-RPC frames are bounded (`wire.MaxFrame`
32 MiB), events are bound to `(plugin, pluginAccount)` so a plugin cannot
touch accounts of other plugins, account ids are host-generated and
validated (`sdk.ValidAccountID`), media files are exchanged in host-chosen
paths inside the plugin's data directory, stderr lines are capped at 1 MiB.

### D-07 (Low) – environment expansion in ScheduleDelete

The delayed deletion builds a `cmd.exe /c` line with `del "<path>"`;
`%NAME%` inside quotes is still expanded, so an installation folder named
e.g. `...\%USERPROFILE%\...` would delete elsewhere. Paths containing `%`,
`"` or line breaks are now skipped (`SafeForCmd`, test `TestSafeForCmd`).

### D-08 (Low) – Android share target accepts the app's own FileProvider

Only `content://` URIs were accepted (good), but URIs of TuxMail's own
non-exported FileProvider are always readable by TuxMail itself: another app
could send `content://org.tuxmail.app.fileprovider/cache/...` and get a
cached attachment of an unrelated mail attached to a draft. Such URIs are now
dropped in `ShareInbox.capture`.

### D-09 (Low) – no FLAG_SECURE with app lock

With the biometric app lock enabled the app-switcher thumbnail still showed
the last screen. `MainActivity` now sets `FLAG_SECURE` whenever the web
layer's app-lock setting (`CapacitorStorage` / `tuxmail.appLock`) is enabled
(checked in `onCreate`, `onResume`, `onPause`). Note: this also blocks
screenshots while the lock is on – intended.

### D-10 (Low) – explicit network security config

`res/xml/network_security_config.xml`: no cleartext, system CAs only (no
user-installed CAs). Matches today's targetSdk defaults but no longer depends
on them.

### D-11 (Low) – PWA notification click target

`sw.js` opened `payload.url` even if it pointed to another origin; the target
is now restricted to the service worker scope. The SW never caches `/api/`
and serves only same-origin requests.

### D-12 (Low) – AppImage AppRun trusted an inherited APPDIR

`APPDIR=${APPDIR:-...}` let a value inherited from another AppImage (or a
caller) choose the directory the window's WebKit helper processes are started
from (relative path patched into the bundled WebKit). `AppRun` now always uses
its own directory.

### D-13 (Low) – unpinned build tools

`package-appimage.sh` downloads linuxdeploy, linuxdeploy-plugin-gtk (from
`master`) and appimagetool "continuous" builds. Their hashes were only
printed; they can now be pinned with `LINUXDEPLOY_SHA256`,
`LINUXDEPLOY_PLUGIN_GTK_SHA256`, `APPIMAGETOOL_SHA256` (recommended for
release builds). The WebView2 bootstrapper embedded by
`package-setup.sh` is fetched over HTTPS from Microsoft; verifying its
Authenticode signature (e.g. `osslsigncode verify`) is recommended.

### Reviewed without findings (informational)

* **Wails bindings / asset server**: `processMessage` validates the origin of
  the top frame and the sender (`validBindingOrigin`); iframes/srcdoc content
  and foreign origins cannot call `window.go.*`. The asset handler (API routes
  inside the desktop) is served through the WebView's custom scheme, not a TCP
  port. `BrowserOpenURL` is filtered by Wails (no `file:`, `javascript:`,
  shell metacharacters) and the frontend only passes `http(s)` URLs
  (`platform.openExternal`). Bound methods with local paths (import/export,
  backup targets) are desktop-only by design (the user owns the machine) and
  refused in server mode.
* **Desktop test hook**: off unless `TUXMAIL_DESKTOP_TEST_LISTEN` is a
  loopback address **and** `TUXMAIL_DESKTOP_TEST_TOKEN` has ≥ 16 characters;
  constant-time token check on every request; a custom header also defeats
  browser-based (DNS rebinding) access. Whoever can set the environment of the
  process already controls it, so it is acceptable in release builds.
* **OAuth loopback**: `127.0.0.1:0` (+`[::1]` same port), PKCE S256, random
  `state`, timeout, escaped error page.
* **Data directory / logs**: `0700` data dir, `0600` log/prefs files,
  portable detection only next to the executable.
* **Registry**: all paths quoted (`UninstallString`, `DisplayIcon`, mailto
  command, Run key); the mailto handler does not pass `%1` yet (no argument
  injection surface). NSIS script: same quoting.
* **Payload extraction**: `safeName` rejects absolute paths, `..`, drive and
  stream syntax, reserved names; directory entries ignored; the install
  record is re-validated on read; uninstall never deletes outside the folder.
* **Elevation arguments**: `QuoteArgs` follows `CommandLineToArgvW` rules
  (tested); the elevated child re-validates the destination (`CheckDir`).
* **Linux packages**: no maintainer scripts, no setuid, binaries `0755`,
  directories `0755`; packages can be GPG-signed.
* **Docker**: non-root user `10001`, no secrets in the image; all writes go
  to `/data` (a read-only root filesystem was not tested).
* **systemd**: strict sandbox (`ProtectSystem=strict`, `NoNewPrivileges`,
  empty capability set, syscall filter, `UMask=0027`).
* **Android**: only `MainActivity` exported (launcher, `tuxmail://`, share,
  `mailto:`); receiver/services/provider not exported; `allowBackup=false` +
  data extraction rules; Keystore AES-256-GCM with random 12-byte IV and the
  entry name as AAD; `PendingIntent`s explicit, immutable except the
  RemoteInput reply (required); inline reply and mark-read use the stored
  device token, refuse non-https server URLs and do not follow redirects;
  deep links are mapped to a fixed route allow-list (`navigation.ts`); the web
  layer keeps the device token in secure storage only (localStorage holds no
  secrets).

### Residual risks / recommendations

1. **Plugin integrity**: plugins are not signed or hash-checked. Recommended:
   ship a manifest with SHA-256 hashes (generated in `build/plugins.sh`, next
   to the main binary) and verify before launch; long term, sign releases.
2. **Plugins share the OS user**: every plugin can read the other plugins'
   sessions and the TuxMail data directory. Sandboxing (separate user,
   seccomp/AppContainer) would be needed to isolate them.
3. **Unsigned Windows binaries**: no Authenticode signature, so UAC shows
   "Unknown publisher" and SmartScreen cannot vouch for the files. Signing is
   the most important remaining supply-chain step.
4. **Classic NSIS installer** (`build/windows/installer.nsi`): still uses
   `$PLUGINSDIR` under the user's `%TEMP%` while elevated and does not protect
   custom all-users folders; prefer the Go setup, or retire the NSIS script.
5. **Keystore key not bound to user authentication**: the app lock is a UI
   gate; the device token can be decrypted without biometrics by code running
   as the app. Binding a key with `setUserAuthenticationRequired` would make
   background actions (inline reply) impossible without unlock – a trade-off.
6. **Windows-only fixes (D-02 … D-05, D-07) are compiled and unit-tested
   but were not run on Windows** in this audit; a manual smoke test (install
   per user/all users/custom folder, uninstall, WebView2 missing) is required
   before release.
7. **Progress file** of the elevated child stays in the user's `%TEMP%`
   (written via temp file + rename; name contains PID and nanoseconds). The
   parent must read it, so it cannot be private; low risk.
