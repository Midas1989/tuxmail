TuxMail @VERSION@ - portabel / portable (Windows @ARCH@)
=======================================================

DEUTSCH
-------
Dieses ZIP enthält TuxMail im portablen Modus. Die leere Datei "portable"
neben TuxMail.exe schaltet ihn ein: Alle Daten (Konten, Mails, Einstellungen)
liegen im Ordner "data" neben TuxMail.exe. Es werden keine Registry-Einträge
und keine Dateien außerhalb dieses Ordners angelegt. Den Ordner kann man z. B.
auf einen USB-Stick kopieren.

Start:   ZIP vollständig entpacken (nicht direkt aus dem ZIP starten) und
         TuxMail.exe doppelklicken.

Zugangsdaten werden im portablen Modus nicht in der Windows-Anmeldeinformations-
verwaltung, sondern verschlüsselt in "data\secrets.vault" gespeichert. Beim Start
fragt TuxMail nach dem Master-Passwort. Ohne dieses Passwort lassen sich die
Zugangsdaten nicht wiederherstellen - bitte gut aufbewahren.

Windows SmartScreen: Solange TuxMail-Builds nicht mit einem Code-Signing-
Zertifikat signiert sind, zeigt Windows beim ersten Start eventuell
"Der Computer wurde durch Windows geschützt". Prüfen Sie die Datei (Prüfsummen
in checksums.txt der Release-Seite) und klicken Sie dann auf "Weitere
Informationen" -> "Trotzdem ausführen". Wurde das ZIP aus dem Internet geladen,
hilft auch: Rechtsklick auf das ZIP -> Eigenschaften -> "Zulassen" vor dem Entpacken.

Voraussetzungen: Windows 10 22H2 oder Windows 11 mit Microsoft Edge WebView2
(dort vorinstalliert; sonst https://go.microsoft.com/fwlink/p/?LinkId=2124703).

Installierten Modus statt portabel: Datei "portable" und Ordner "data" löschen -
dann liegen die Daten in %APPDATA%\TuxMail. Für eine Installation mit Startmenü
und Deinstallation gibt es TuxMail-@VERSION@-windows-@ARCH@-setup.exe.

Hinweis: TuxMail.exe ist ein Fensterprogramm. Für die Kommandozeile
("tuxmail serve", "tuxmail user ...") gibt es tuxmail_@VERSION@_windows_@ARCH@.zip.
Lizenz: GNU AGPL v3 oder später, siehe LICENSE.txt und NOTICE.txt.


ENGLISH
-------
This ZIP contains TuxMail in portable mode. The empty file "portable" next to
TuxMail.exe enables it: all data (accounts, mail, settings) is stored in the
"data" folder next to TuxMail.exe. No registry entries and no files outside
this folder are created. You can copy the folder to a USB stick, for example.

Start:   Extract the whole ZIP (do not run it from inside the ZIP) and
         double-click TuxMail.exe.

In portable mode, credentials are not stored in the Windows Credential Manager
but encrypted in "data\secrets.vault". TuxMail asks for the master password on
start. Credentials cannot be recovered without it - keep it safe.

Windows SmartScreen: as long as TuxMail builds are not signed with a code
signing certificate, Windows may show "Windows protected your PC" on first
start. Verify the file (checksums in checksums.txt on the release page), then
click "More info" -> "Run anyway". If the ZIP was downloaded from the internet,
you can also right-click the ZIP -> Properties -> "Unblock" before extracting.

Requirements: Windows 10 22H2 or Windows 11 with Microsoft Edge WebView2
(preinstalled there; otherwise https://go.microsoft.com/fwlink/p/?LinkId=2124703).

Installed mode instead of portable: delete the file "portable" and the folder
"data" - data is then stored in %APPDATA%\TuxMail. For an installation with
Start menu entry and uninstaller use TuxMail-@VERSION@-windows-@ARCH@-setup.exe.

Note: TuxMail.exe is a GUI program. For the command line ("tuxmail serve",
"tuxmail user ...") use tuxmail_@VERSION@_windows_@ARCH@.zip.
License: GNU AGPL v3 or later, see LICENSE.txt and NOTICE.txt.
