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
set -euo pipefail
umask 077

DATA_DIR=/var/lib/tuxmail
BACKUP_DIR=/var/backups/tuxmail
SERVICE=tuxmail.service
EXT=tar.zst
COMPRESS_OPT=--zstd
AGE_ARGS=""

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
