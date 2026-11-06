#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Beispiel (aus der Ansible-Rolle tuxmail erzeugt) – installieren nach /usr/local/sbin/tuxmail-backup (0750).
#
# Sichert /var/lib/tuxmail als komprimiertes tar-Archiv nach /var/backups/tuxmail.
#
#   tuxmail-backup [--full] [--stop] LABEL KEEP [TAG]
#
#   Standard: nur Einstellungen (tuxmail backup-settings), keine Mails, Chats, Kalender oder Kontakte;
#   der Dienst läuft weiter. Alles, was das Archiv enthält, steht in internal/backup/settings.go.
#   --full  ganzes Datenverzeichnis, für den Rückweg bei einem Versionswechsel (pre-upgrade)
#   --stop  Dienst tuxmail während der Sicherung anhalten (konsistente SQLite-Dateien)
#   LABEL   z. B. "scheduled", "pre-upgrade", "manual" (nur a-z, 0-9, -)
#   KEEP    Anzahl der Archive dieses Labels, die behalten werden
#   TAG     optionaler Zusatz im Dateinamen (z. B. Version)
#
# Ergebnis: /var/backups/tuxmail/tuxmail-LABEL-JJJJMMTT-HHMMSS[-TAG].tar.zst.age
#
# Verschlüsselung: Das Archiv enthält master.key. Den öffentlichen Schlüssel legt die Oberfläche in
# /var/lib/tuxmail/backup/recipients.txt ab (Einstellungen → Administration → Sicherung). Ohne ihn endet das
# Skript mit Exit 3 und ohne Datei, bevor der Dienst angehalten wird. Unverschlüsselt nur ausdrücklich:
# TUXMAIL_BACKUP_ALLOW_PLAINTEXT=1 tuxmail-backup ...
set -euo pipefail
umask 077

DATA_DIR=/var/lib/tuxmail
BACKUP_DIR=/var/backups/tuxmail
SERVICE=tuxmail.service
EXT=tar.zst
COMPRESS_OPT=--zstd
BASE_EXT="$EXT"
KEY_DIR=/var/lib/tuxmail/backup
KEY_FILE="$KEY_DIR/recipients.txt"
STATUS_FILE="$KEY_DIR/status.json"
SERVICE_USER=tuxmail
TUXMAIL_BIN=/opt/tuxmail/current/tuxmail
ALLOW_PLAINTEXT=0

stop=0
full=0
while :; do
    case "${1:-}" in
        --stop) stop=1 ;;
        --full) full=1 ;;
        *) break ;;
    esac
    shift
done
label=${1:-manual}
keep=${2:-7}
tag=${3:-}

case "$label" in
    ''|*[!a-z0-9-]*) echo "ungültiges Label: $label" >&2; exit 2 ;;
esac
case "$keep" in
    ''|*[!0-9]*) echo "ungültige Anzahl: $keep" >&2; exit 2 ;;
esac
if [ "$keep" -lt 1 ]; then
    keep=1
fi
tag=$(printf '%s' "$tag" | tr -c 'A-Za-z0-9._-' '_')


# Ergebnis des Laufs für die Oberfläche. Das Datenverzeichnis gehört dem Dienstbenutzer, deshalb schreibt
# auch er die Datei: Als root in ein fremdes Verzeichnis zu schreiben, ließe sich über Symlinks umlenken.
result=failed
encrypted=false
tmp=""
stage=""
restart=0
as_service() {
    if [ "$(id -u)" = 0 ]; then
        runuser -u "$SERVICE_USER" -- "$@"
    else
        "$@"
    fi
}
write_status() {
    [ -d "$KEY_DIR" ] || return 0
    local writer='t=$(mktemp "$1.XXXXXX") && cat >"$t" && chmod 0640 "$t" && mv -f "$t" "$1"'
    printf '{"finishedAt":"%s","label":"%s","result":"%s","encrypted":%s}\n' \
        "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$label" "$result" "$encrypted" |
        as_service sh -c "$writer" sh "$STATUS_FILE"
}
cleanup() {
    [ -z "$tmp" ] || rm -f "$tmp"
    # Die Einstellungen liegen hier unverschlüsselt, sie bleiben nie liegen.
    [ -z "$stage" ] || rm -rf "$stage"
    if [ "$restart" -eq 1 ]; then
        systemctl start "$SERVICE" || echo "WARNUNG: $SERVICE konnte nicht gestartet werden" >&2
    fi
    write_status || echo "WARNUNG: Ergebnis konnte nicht nach $STATUS_FILE geschrieben werden" >&2
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP

# Der öffentliche Schlüssel kommt aus der Oberfläche (Einstellungen → Administration → Sicherung), die ihn in
# KEY_FILE ablegt. Die Datei gehört dem Dienstbenutzer: Zeilen werden nur als age-Empfänger übernommen, eine
# andere Zeile stoppt den Lauf, statt sie zu übergehen oder auszugeben.
AGE_ARGS=()
key_state=missing
if [ -L "$KEY_FILE" ] || { [ -e "$KEY_FILE" ] && [ ! -f "$KEY_FILE" ]; }; then
    key_state=invalid
elif [ -f "$KEY_FILE" ]; then
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in
            '' | '#'*) ;;
            *)
                if [[ ! "$line" =~ ^age1[0-9a-z]+$ ]]; then
                    key_state=invalid
                elif [ "$key_state" != invalid ]; then
                    AGE_ARGS+=(-r "$line")
                    key_state=ok
                fi
                ;;
        esac
    done <"$KEY_FILE"
fi

# Klartext-Sicherungen nur ausdrücklich (Re-Audit ND-14): das Archiv enthält master.key (entschlüsselt
# alle Benutzer-Geheimnisse). Die Prüfungen stehen vor dem Anhalten des Dienstes. Eine beschädigte
# Schlüsseldatei ist kein bewusster Verzicht, sie stoppt den Lauf auch mit erlaubtem Klartext.
if [ "$key_state" = invalid ]; then
    result=invalid_key
    echo "Sicherung abgelehnt: $KEY_FILE enthält keine gültigen öffentlichen age-Schlüssel." >&2
    echo "Lege in der Oberfläche unter Einstellungen → Administration → Sicherung einen neuen Schlüssel an." >&2
    exit 3
fi
if [ "$key_state" = ok ]; then
    if ! command -v age >/dev/null 2>&1; then
        echo "Sicherung abgelehnt: das Programm age ist nicht installiert." >&2
        exit 1
    fi
    encrypted=true
    EXT="${BASE_EXT}.age"
elif [ "${TUXMAIL_BACKUP_ALLOW_PLAINTEXT:-$ALLOW_PLAINTEXT}" != 1 ]; then
    result=no_key
    echo "Sicherung abgelehnt: Es gibt noch keinen Backup-Schlüssel, es wurde nichts gesichert." >&2
    echo "Erzeuge ihn in der Oberfläche unter Einstellungen → Administration → Sicherung." >&2
    echo "Unverschlüsselt (das Archiv enthält master.key) nur ausdrücklich: TUXMAIL_BACKUP_ALLOW_PLAINTEXT=1" >&2
    echo "bzw. tuxmail_backup_allow_plaintext: true in Ansible." >&2
    exit 3
fi

ts=$(date +%Y%m%d-%H%M%S)
name="tuxmail-${label}-${ts}${tag:+-$tag}.${EXT}"
target="${BACKUP_DIR}/${name}"
tmp="${BACKUP_DIR}/.${name}.partial"

if [ "$stop" -eq 1 ] && systemctl is-active --quiet "$SERVICE"; then
    systemctl stop "$SERVICE"
    restart=1
fi

mkdir -p "$BACKUP_DIR"
# Nur ein Lauf zugleich: Reste eines hart beendeten Laufs gehören niemandem mehr.
exec 9>"$BACKUP_DIR/.lock"
flock 9
as_service mkdir -p "$KEY_DIR"
rm -rf "$KEY_DIR"/.staging.*

src_dir=$(dirname "$DATA_DIR")
if [ "$full" -eq 0 ]; then
    # Bereinigte, konsistente Kopie im privaten Verzeichnis (0700, gehört dem Dienstbenutzer).
    stage=$(as_service mktemp -d "$KEY_DIR/.staging.XXXXXX")
    as_service "$TUXMAIL_BIN" backup-settings --data-dir "$DATA_DIR" --out "$stage"
    src_dir=$stage
fi
# Das Archiv ist nur für root lesbar (umask 077). --full enthält das ganze Datenverzeichnis.
if [ "$encrypted" = true ]; then
    # shellcheck disable=SC2086
    tar $COMPRESS_OPT -C "$src_dir" -cpf - "$(basename "$DATA_DIR")" | age "${AGE_ARGS[@]}" -o "$tmp"
else
    # shellcheck disable=SC2086
    tar $COMPRESS_OPT -C "$src_dir" -cpf "$tmp" "$(basename "$DATA_DIR")"
fi
mv "$tmp" "$target"
tmp=""
result=ok
echo "Sicherung erstellt: $target"

# Alte Sicherungen dieses Labels entfernen, verschlüsselte und unverschlüsselte zusammen
# (Dateinamen enthalten keine Leerzeichen).
# shellcheck disable=SC2012
ls -1t "$BACKUP_DIR"/tuxmail-"$label"-*."$BASE_EXT"* 2>/dev/null | tail -n +"$((keep + 1))" | while read -r old; do
    rm -f -- "$old"
    echo "Alte Sicherung entfernt: $old"
done
