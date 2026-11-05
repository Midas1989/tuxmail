#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Beispiel (aus der Ansible-Rolle tuxmail erzeugt) – installieren nach /usr/local/sbin/tuxmail-backup (0750).
#
# Sichert /var/lib/tuxmail als komprimiertes tar-Archiv nach /var/backups/tuxmail.
#
#   tuxmail-backup [--stop] LABEL KEEP [TAG]
#
#   --stop  Dienst tuxmail während der Sicherung anhalten (konsistente SQLite-Dateien)
#   LABEL   z. B. "scheduled", "pre-upgrade", "manual" (nur a-z, 0-9, -)
#   KEEP    Anzahl der Archive dieses Labels, die behalten werden
#   TAG     optionaler Zusatz im Dateinamen (z. B. Version)
#
# Ergebnis: /var/backups/tuxmail/tuxmail-LABEL-JJJJMMTT-HHMMSS[-TAG].tar.zst
#
# Verschlüsselung: Das Archiv enthält master.key. Trage unten AGE_ARGS="-r age1..." ein (und hänge
# ".age" an EXT) oder starte bewusst im Klartext: TUXMAIL_BACKUP_ALLOW_PLAINTEXT=1 tuxmail-backup ...
# Ohne beides bricht das Skript ab, bevor der Dienst angehalten wird.
set -euo pipefail
umask 077

DATA_DIR=/var/lib/tuxmail
BACKUP_DIR=/var/backups/tuxmail
SERVICE=tuxmail.service
EXT=tar.zst
COMPRESS_OPT=--zstd
AGE_ARGS=""
ALLOW_PLAINTEXT=0

stop=0
if [ "${1:-}" = "--stop" ]; then
    stop=1
    shift
fi
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

# Klartext-Sicherungen nur ausdrücklich (Re-Audit ND-14): das Archiv enthält master.key (entschlüsselt
# alle Benutzer-Geheimnisse). Ohne age-Empfänger läuft das Skript nur mit
# TUXMAIL_BACKUP_ALLOW_PLAINTEXT=1 (Umgebung) bzw. tuxmail_backup_allow_plaintext: true (Ansible).
# Die Prüfung steht vor dem Anhalten des Dienstes.
if [ -z "$AGE_ARGS" ] && [ "${TUXMAIL_BACKUP_ALLOW_PLAINTEXT:-$ALLOW_PLAINTEXT}" != 1 ]; then
    echo "Sicherung abgelehnt: keine age-Empfänger konfiguriert, das Archiv wäre unverschlüsselt (enthält master.key)." >&2
    echo "Setze tuxmail_backup_age_recipients (öffentliche age-Schlüssel) oder erlaube Klartext ausdrücklich mit" >&2
    echo "TUXMAIL_BACKUP_ALLOW_PLAINTEXT=1 (bzw. tuxmail_backup_allow_plaintext: true in Ansible)." >&2
    exit 3
fi

ts=$(date +%Y%m%d-%H%M%S)
name="tuxmail-${label}-${ts}${tag:+-$tag}.${EXT}"
target="${BACKUP_DIR}/${name}"
tmp="${BACKUP_DIR}/.${name}.partial"

restart=0
cleanup() {
    rm -f "$tmp"
    if [ "$restart" -eq 1 ]; then
        systemctl start "$SERVICE" || echo "WARNUNG: $SERVICE konnte nicht gestartet werden" >&2
    fi
}
trap cleanup EXIT
trap 'exit 1' INT TERM HUP

if [ "$stop" -eq 1 ] && systemctl is-active --quiet "$SERVICE"; then
    systemctl stop "$SERVICE"
    restart=1
fi

mkdir -p "$BACKUP_DIR"
# Enthält master.key, vapid.json, server.db und users/<id>/ – Archiv nur für root lesbar (umask 077).
if [ -n "$AGE_ARGS" ]; then
    # shellcheck disable=SC2086
    tar $COMPRESS_OPT -C "$(dirname "$DATA_DIR")" -cpf - "$(basename "$DATA_DIR")" | age $AGE_ARGS -o "$tmp"
else
    tar $COMPRESS_OPT -C "$(dirname "$DATA_DIR")" -cpf "$tmp" "$(basename "$DATA_DIR")"
fi
mv "$tmp" "$target"
echo "Sicherung erstellt: $target"

# Alte Sicherungen dieses Labels entfernen (Dateinamen enthalten keine Leerzeichen).
# shellcheck disable=SC2012
ls -1t "$BACKUP_DIR"/tuxmail-"$label"-*."$EXT" 2>/dev/null | tail -n +"$((keep + 1))" | while read -r old; do
    rm -f -- "$old"
    echo "Alte Sicherung entfernt: $old"
done
