TuxMail @VERSION@ - portable (Linux @ARCH@)
==========================================

DEUTSCH
-------
Dieses Archiv enthält TuxMail im portablen Modus. Die leere Datei "portable"
neben dem Programm schaltet ihn ein: Alle Daten (Konten, Mails, Einstellungen)
liegen im Ordner "data/" neben "tuxmail", es wird nichts außerhalb dieses
Ordners gespeichert. Den Ordner kann man z. B. auf einen USB-Stick kopieren.

Start:    ./tuxmail
          (oder: ./tuxmail --data-dir /anderer/ordner)

Zugangsdaten werden im portablen Modus nicht im System-Schlüsselbund, sondern
verschlüsselt in "data/secrets.vault" gespeichert. Beim Start fragt TuxMail
nach dem Master-Passwort. Ohne dieses Passwort lassen sich die Zugangsdaten
nicht wiederherstellen - bitte gut aufbewahren.

Voraussetzungen: GTK 3 und WebKitGTK 4.1 müssen installiert sein, z. B.
  Debian/Ubuntu:  sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0
  Fedora:         sudo dnf install gtk3 webkit2gtk4.1
Ohne diese Bibliotheken das AppImage verwenden (enthält alles Nötige).

Installierten Modus statt portabel: Datei "portable" und Ordner "data/"
löschen - dann liegen die Daten in ~/.local/share/tuxmail.

Das Programm enthält auch die Kommandozeile (./tuxmail help).
Lizenz: GNU AGPL v3 oder später, siehe LICENSE und NOTICE.


ENGLISH
-------
This archive contains TuxMail in portable mode. The empty file "portable" next
to the program enables it: all data (accounts, mail, settings) is stored in the
"data/" folder next to "tuxmail" and nothing is written outside this folder.
You can copy the folder to a USB stick, for example.

Start:    ./tuxmail
          (or: ./tuxmail --data-dir /another/folder)

In portable mode, credentials are not stored in the system keyring but
encrypted in "data/secrets.vault". TuxMail asks for the master password on
start. Credentials cannot be recovered without it - keep it safe.

Requirements: GTK 3 and WebKitGTK 4.1 must be installed, e.g.
  Debian/Ubuntu:  sudo apt install libgtk-3-0 libwebkit2gtk-4.1-0
  Fedora:         sudo dnf install gtk3 webkit2gtk4.1
Without these libraries use the AppImage (it bundles everything needed).

Installed mode instead of portable: delete the file "portable" and the folder
"data/" - data is then stored in ~/.local/share/tuxmail.

The program also contains the command line interface (./tuxmail help).
License: GNU AGPL v3 or later, see LICENSE and NOTICE.
