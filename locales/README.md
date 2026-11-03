<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->
# TuxMail language files / Sprachdateien

**English** · [Deutsch](#deutsch)

This directory is the single source of truth for all user-visible texts of
TuxMail. The frontend imports the files through the Vite alias `$locales`
(see `frontend/vite.config.ts`); the Go core can embed them as well.

| File | Language | Role |
|---|---|---|
| `en.json` | English | **Reference and fallback** – always complete |
| `de.json` | Deutsch | complete translation |

## Structure

- Nested keys grouped by area: `common`, `nav`, `mail`, `chat`, `calendar`,
  `contacts`, `files`, `settings`, `search`, `compose`, `workflow`, `errors`,
  `notifications`, `sendcheck`, `onboarding`. In code a key is addressed with
  dots, e.g. `t('mail.list.count', { count: 3 })`.
- Every file starts with a `meta` block:
  ```json
  "meta": { "code": "fr", "name": "French", "nativeName": "Français", "direction": "ltr" }
  ```
- **Placeholders:** `{name}` – must be identical in every language.
- **Plurals:** the plain key holds the general form, optional variants use the
  [Intl.PluralRules](https://developer.mozilla.org/docs/Web/JavaScript/Reference/Global_Objects/Intl/PluralRules)
  category as suffix: `_zero`, `_one`, `_two`, `_few`, `_many`, `_other`.
  ```json
  "count": "{count} messages",
  "count_one": "{count} message"
  ```
- Dates, times, numbers and relative times are **not** translated here; they
  are formatted with `Intl` (DateTimeFormat, NumberFormat, RelativeTimeFormat)
  using the active language and the user's “Language & region” settings.
- Fallback chain at runtime: selected language → `en` → key.

## Adding a language

1. Copy `en.json` to `<code>.json` (BCP 47 code, e.g. `fr`, `pt-BR`).
2. Translate all values; keep keys and `{placeholders}` unchanged.
3. Set `meta.code`, `meta.name` (English name), `meta.nativeName` and
   `meta.direction` (`ltr` or `rtl`); remove `"reference": true`.
4. Register the file in `frontend/src/lib/i18n/index.svelte.ts`
   (`builtinRaw`) – or let the server deliver it at runtime
   (`GET /api/locales`, `GET /api/locales/<code>`); delivered files are merged
   on top of the built-in ones, so they can also override single texts.
5. Run `npm test` in `frontend/`: the tests check that every language has
   exactly the keys of `en.json` and the same placeholders.

---

## Deutsch

Dieses Verzeichnis ist die einzige Quelle für alle sichtbaren Texte von
TuxMail. Das Frontend bindet die Dateien über den Vite-Alias `$locales` ein
(siehe `frontend/vite.config.ts`); der Go-Kern kann sie ebenfalls einbetten.

| Datei | Sprache | Rolle |
|---|---|---|
| `en.json` | Englisch | **Referenz und Rückfallsprache** – immer vollständig |
| `de.json` | Deutsch | vollständige Übersetzung |

## Aufbau

- Verschachtelte Schlüssel nach Bereichen (siehe oben). Im Code werden sie mit
  Punkten angesprochen, z. B. `t('mail.list.count', { count: 3 })`.
- Jede Datei beginnt mit einem `meta`-Block (`code`, `name`, `nativeName`,
  `direction`).
- **Platzhalter:** `{name}` – in allen Sprachen identisch.
- **Mehrzahl:** Der einfache Schlüssel enthält die allgemeine Form; Varianten
  hängen die Kategorie von `Intl.PluralRules` an (`_one`, `_few`, …).
- Datum, Uhrzeit, Zahlen und relative Zeiten werden **nicht** übersetzt, sondern
  per `Intl` mit der aktiven Sprache und den Einstellungen unter
  „Sprache & Region“ formatiert.
- Rückfallkette zur Laufzeit: gewählte Sprache → `en` → Schlüssel.

## Neue Sprache hinzufügen

1. `en.json` nach `<code>.json` kopieren (BCP-47-Code, z. B. `fr`, `pt-BR`).
2. Alle Werte übersetzen; Schlüssel und `{Platzhalter}` unverändert lassen.
3. `meta.code`, `meta.name` (englischer Name), `meta.nativeName` und
   `meta.direction` (`ltr` oder `rtl`) setzen; `"reference": true` entfernen.
4. Datei in `frontend/src/lib/i18n/index.svelte.ts` (`builtinRaw`) eintragen –
   oder vom Server zur Laufzeit ausliefern lassen (`GET /api/locales`,
   `GET /api/locales/<code>`); gelieferte Dateien werden über die eingebauten
   gelegt und können so auch einzelne Texte überschreiben.
5. Im Ordner `frontend/` `npm test` ausführen: Die Tests prüfen, dass jede
   Sprache genau die Schlüssel von `en.json` und dieselben Platzhalter hat.
