<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# TuxMail für Android

Native Android-App von TuxMail als **Thin Client** (KONZEPT 15.9, Stufe A,
„Weg 2“): Capacitor bündelt die vorhandene Svelte-Oberfläche aus `frontend/`
in ein APK, die App verbindet sich mit **beliebigen TuxMail-Servern**
(`tuxmail serve`). Synchronisiert wird auf dem Server; die App zeigt an, sendet
über die Server-API und empfängt native Push-Benachrichtigungen.

```
mobile/
├── package.json            Capacitor 8 + offizielle Plugins (exakte Versionen), Version der App
├── capacitor.config.ts     appId org.tuxmail.app, webDir ../frontend/dist, androidScheme https
├── android/                Gradle-Projekt (von `cap add android` erzeugt, angepasst, eingecheckt)
│   └── app/src/
│       ├── main/           MainActivity, TuxMail-Plugin (Kotlin), Benachrichtigungen, Teilen, Ressourcen
│       ├── foss/           UnifiedPush (F-Droid, ohne Google)
│       └── play/           Firebase Cloud Messaging (Google Play)
├── scripts/build.sh        Frontend-Build → cap sync → Gradle
├── scripts/generate-android-icons.py   Launcher-/Splash-/Store-Icons aus assets/branding
├── fastlane/metadata/android/{en-US,de-DE}/   Store-Texte (F-Droid / Play)
└── SERVER_TODO.md          nötige Server-Erweiterungen (FCM, Schnellantwort, WebSocket-Token)
```

Die Web-Seite der App liegt in `frontend/src/lib/native/` (nur im Capacitor-
WebView geladen, separater Chunk): Server verbinden, Geräteanmeldung,
Profilverwaltung, Push-Registrierung, Teilen-Ziel, Zurück-Taste, Deep Links,
Systemleisten, App-Sperre und die App-Einstellungen unter `#/native-settings`.

## Funktionen

- **Erster Start „Server verbinden“**: Adresse wird über `GET /healthz` und
  `GET /api/auth/config` geprüft (Erkennung am Feld `product: "tuxmail"`; die
  Versionsangabe liefert der Server nur mit `security.expose_version`). Anmeldung per **Gerätebestätigung** (RFC 8628:
  Custom Tab mit `verification_uri_complete`, Abfrage von `/api/device/token`)
  oder direkt mit Benutzername, Passwort und TOTP (`/api/device/login`).
- **Geräte-Token** im Android-Keystore (AES-256-GCM, eigener kleiner Kotlin-Code
  statt veralteter EncryptedSharedPreferences oder Fremd-Plugins). Mehrere
  Server-Profile sind im Datenmodell vorgesehen (Wechsel = Neustart der Oberfläche).
- **HTTP mit Bearer-Token** statt Cookies; `CapacitorHttp` umgeht CORS.
  Bei `401` wird das Token verworfen und die Anmeldung erneut angezeigt.
- **Push**: `foss` = UnifiedPush (z. B. ntfy), `play` = FCM. Kanäle Mail, Chats,
  Termine, Erinnerungen; eine Benachrichtigung pro Konversation (MessagingStyle),
  Gruppierung je Kanal, Tippen öffnet die Konversation, **Antworten** (RemoteInput)
  und **Als gelesen markieren** direkt aus der Benachrichtigung.
- **Teilen-Ziel**: Text, Links, Bilder und beliebige Dateien (auch mehrere) sowie
  `mailto:`-Links öffnen den Editor mit Anhängen. Es werden nur `content://`-URIs
  angenommen (Schutz vor dem Auslesen privater App-Dateien).
- **App-Sperre** (optional): BiometricPrompt mit Fingerabdruck, Gesicht oder
  Displaysperre; Sperre sofort oder nach 1/5/15 Minuten im Hintergrund.
- Zurück-Taste (Dialog → Antwort → Seitenleiste → Konversation → Posteingang →
  App minimieren), Edge-to-Edge mit Safe-Area-Abständen, Statusleiste hell/dunkel
  passend zum Thema, Splash-Screen mit dem Pinguin, adaptive Icons mit
  Monochrom-Variante (Android 13+), Sprache pro App (Android 13+).
- **Offline** (wie die Desktop-App „Über den Server“, KONZEPT 15.9): Ordner,
  Mailliste, Mails samt Anhängen (nach Regel), Chats, Kalender und Kontakte
  liegen je Server-Profil in IndexedDB im App-privaten Speicher
  (`frontend/src/lib/native/offline`). Ohne Netz: lesen, im Geladenen suchen,
  schreiben mit Anhängen, markieren, archivieren/löschen – alles wartet im
  Postausgang und geht beim Wiederverbinden genau einmal raus
  (`Idempotency-Key` = Postausgangs-Kennung, „unklar“ bei 5xx/Antwortverlust,
  Versandprüfung → zurück als Entwurf, Konflikte sichtbar). Einstellungen unter
  App-Einstellungen > Offline: Größe, Anhänge im Voraus, „Ältere Mails nur als
  Kopfzeilen“ (`mail.headersOnlyOlderThanDays`), WLAN/mobil, Intervall.
  Hintergrund: `OfflineSyncWorker` (WorkManager, Akku nicht niedrig, gewähltes
  Netz; Push löst ihn aus) lädt nur lesend in `noBackupFilesDir/offline-staging`
  (höchstens 20 MB), die Oberfläche übernimmt das beim nächsten Start. Mails
  verschickt nur die laufende App. Abmelden/Serverwechsel löschen den Speicher.
  Keine eigene Verschlüsselung der Ablage: siehe
  `docs/security/BASIS-ABWEICHUNGEN.md` Nr. 20. Rundlauf-Test: Chromium im
  Handymodus mit Capacitor-Stubs gegen `tuxmail serve` + `tuxmail devserver`.
- Kein Service Worker in der App (`pwa.ts` prüft `isNativeShell()`), keine
  Tracker, kein Backup der App-Daten.

## Lokal bauen

Voraussetzungen: Node.js 22, JDK 21, Android SDK (Platform 36, Build-Tools 36)
über Android Studio oder `sdkmanager`; `ANDROID_HOME` setzen oder
`android/local.properties` mit `sdk.dir=…` anlegen.

```bash
cd mobile
npm ci
scripts/build.sh                 # fossDebug-APK
scripts/build.sh playDebug       # mit FCM (braucht google-services.json, s. u.)
scripts/build.sh fossRelease playRelease bundlePlayRelease
npx cap open android             # in Android Studio öffnen
```

`scripts/build.sh` baut zuerst das Frontend (`npm run build:capacitor` in `frontend/` – eigene Content-Security-Policy für die App, s. docs/ENTWICKLUNG.md),
kopiert es mit `npx cap sync android` nach `android/app/src/main/assets/public`
und ruft dann Gradle auf. `SKIP_WEB=1` überspringt den Frontend-Build.
Die APKs liegen unter `android/app/build/outputs/apk/<flavor>/<buildType>/`.

Entwicklung gegen einen lokalen Server: `tuxmail serve` hinter HTTPS
(z. B. Caddy) betreiben; unverschlüsseltes `http://` ist nur für `localhost`
erlaubt (Emulator: `adb reverse tcp:8080 tcp:8080`).

### Version

Einzige Quelle ist `"version"` in `mobile/package.json`. Gradle leitet daraus
`versionName` und `versionCode = MAJOR·10000 + MINOR·100 + PATCH` ab (0.1.0 → 100;
Überschreiben mit `TUXMAIL_VERSION_CODE`). Der Release-Workflow prüft, dass der
Git-Tag `v<version>` dazu passt.

### Varianten (productFlavors)

| Variante | Push | Abhängigkeiten | Verteilung |
|---|---|---|---|
| `foss` | UnifiedPush (`org.unifiedpush.android:connector`, Maven Central) | nur freie Bibliotheken | F-Droid, GitHub Releases |
| `play` | Firebase Cloud Messaging | Firebase Messaging (Google Maven) | Google Play |

`google-services.json` wird **nicht** eingecheckt. Für `play` die Datei aus der
Firebase-Konsole nach `android/app/src/play/google-services.json` legen (CI:
Secret `GOOGLE_SERVICES_JSON`). Fehlt sie, baut `play` trotzdem; Push meldet
dann „keine Firebase-Konfiguration“.

Die Anwendungs-ID ist über `TUXMAIL_APP_ID` (Gradle und `capacitor.config.ts`)
änderbar, z. B. für eigene Firmen-Builds.

### Signieren

Release-Builds werden signiert, wenn diese Variablen gesetzt sind (oder in
`android/keystore.properties`, nie einchecken):

```
TUXMAIL_KEYSTORE_FILE=/pfad/release.jks
TUXMAIL_KEYSTORE_PASSWORD=…
TUXMAIL_KEY_ALIAS=tuxmail
TUXMAIL_KEY_PASSWORD=…
```

Schlüssel erzeugen: `keytool -genkeypair -v -keystore release.jks -alias tuxmail -keyalg RSA -keysize 4096 -validity 10000`.
Ohne diese Angaben entsteht ein unsigniertes Release-APK.

## GitHub Actions (`.github/workflows/android.yml`)

- Bei Push/PR: Frontend prüfen, testen, bauen → `cap sync` → `assembleFossDebug`;
  `assemblePlayDebug` nur, wenn das Secret `GOOGLE_SERVICES_JSON` existiert.
  Die APKs werden als Artefakte hochgeladen.
- Bei Tags `v*`: signierte `fossRelease`-APK, `playRelease`-APK und -AAB,
  `SHA256SUMS`, angehängt an das GitHub-Release.
- Secrets: `KEYSTORE_BASE64` (`base64 -w0 release.jks`), `KEYSTORE_PASSWORD`,
  `KEY_ALIAS`, `KEY_PASSWORD`, optional `GOOGLE_SERVICES_JSON`.

## F-Droid

- Gebaut wird nur `foss`: `gradle: [foss]`, Unterverzeichnis `mobile/android`.
  Der Firebase-Code steckt ausschließlich in `src/play` und
  `playImplementation`; das Google-Services-Gradle-Plugin wird mit
  `-Ptuxmail.gms=false` gar nicht erst geladen (in der F-Droid-Metadatei z. B.
  über `gradleprops: [tuxmail.gms=false]`).
- Vor dem Gradle-Build müssen Frontend und Capacitor-Sync laufen, z. B.:

  ```yaml
  Builds:
    - versionName: 0.1.0
      versionCode: 100
      commit: v0.1.0
      subdir: mobile/android/app
      sudo:
        - apt-get update
        - apt-get install -y npm
      gradle:
        - foss
      gradleprops:
        - tuxmail.gms=false
      prebuild:
        - cd ../../../frontend && npm ci && npm run build:capacitor
        - cd ../mobile && npm ci && npx cap sync android
  ```
- **Reproduzierbare Builds**: exakte npm-Versionen und `package-lock.json`,
  feste Gradle-Version im Wrapper, keine Zeitstempel/VCS-Infos im APK
  (`vcsInfo.include = false`, `dependenciesInfo` aus), kein Minify. Gleiches
  Node/JDK verwenden (Node 22, JDK 21) und `SOURCE_DATE_EPOCH` setzen, damit
  F-Droid das Entwickler-signierte APK als reproduzierbar erkennt.
- Store-Texte und Icon liegen in `fastlane/metadata/android/<sprache>/`
  (`title.txt`, `short_description.txt` ≤ 80 Zeichen, `full_description.txt`,
  `changelogs/<versionCode>.txt`, `images/icon.png`); Screenshots gehören nach
  `images/phoneScreenshots/`.
- Anti-Features: keine. Netzwerkzugriff nur zum vom Nutzer gewählten Server und
  zum UnifiedPush-Verteiler.

## Google Play

- `bundlePlayRelease` erzeugt das AAB; Play App Signing nutzen, der
  CI-Schlüssel ist dann der Upload-Schlüssel.
- Benötigte Angaben in der Play Console: Datenschutzerklärung, Data-Safety
  („E-Mail-Inhalte und Nachrichten werden nur zum eigenen Server übertragen;
  FCM-Token an Google“), Zielgruppe, Berechtigung `POST_NOTIFICATIONS`.
- Für neue Entwicklerkonten gilt die Testpflicht (geschlossener Test) vor der
  Veröffentlichung; außerdem die künftige Entwickler-Verifizierung von Google
  auch für Sideload-APKs beachten (KONZEPT 15.9).
- Store-Texte können per `fastlane supply` aus `fastlane/metadata/android`
  hochgeladen werden.

## Grenzen / offene Punkte

- Mehrere Server gleichzeitig: Datenmodell und Umschalten vorhanden, gleichzeitige
  Push-Registrierung für mehrere Profile noch nicht. Der Offline-Speicher gilt
  nur für das aktive Profil (Wechsel löscht ihn).
- Offline: eingebettete Bilder (`cid:`) im Mailtext erscheinen ohne Netz nicht;
  wartende Mails gehen erst raus, wenn die App läuft.
- Serverseitige Voraussetzungen (FCM-Versand, `/reply`, WebSocket-Token,
  Bestätigungsseite `/device`) siehe [SERVER_TODO.md](SERVER_TODO.md).
- iOS folgt mit demselben Capacitor-Projekt (`npx cap add ios`, Mac nötig).
