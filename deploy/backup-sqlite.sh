#!/bin/bash
# Онлайн-бэкап SQLite через .backup (корректно при WAL, в отличие от cp).
# Использование:
#   backup-sqlite.sh [DB_PATH] [BACKUP_DIR] [KEEP]
# Пример:
#   ./backup-sqlite.sh /opt/cs2bot/bot.db /opt/cs2bot/backups 7
# Для cron (ежедневно в 04:00):
#   0 4 * * * /opt/cs2bot/backup-sqlite.sh /opt/cs2bot/bot.db /opt/cs2bot/backups 7 >>/var/log/cs2bot-backup.log 2>&1
set -euo pipefail

DB_PATH="${1:-/opt/cs2bot/bot.db}"
BACKUP_DIR="${2:-/opt/cs2bot/backups}"
KEEP="${3:-7}"

command -v sqlite3 >/dev/null || {
  echo "ОШИБКА: sqlite3 не установлен (apt install sqlite3)" >&2
  exit 1
}
[ -f "$DB_PATH" ] || {
  echo "ОШИБКА: БД не найдена: $DB_PATH" >&2
  exit 1
}

mkdir -p "$BACKUP_DIR"
STAMP="$(date +%F-%H%M)"
TMP="$BACKUP_DIR/.bot-$STAMP.db.tmp"
OUT="$BACKUP_DIR/bot-$STAMP.db.gz"

sqlite3 "$DB_PATH" ".backup '$TMP'"
gzip -c "$TMP" > "$OUT"
rm -f "$TMP"

# Ротация: оставляем KEEP последних архивов.
ls -1t "$BACKUP_DIR"/bot-*.db.gz | tail -n +"$((KEEP + 1))" | xargs -r rm -f

echo "OK: $OUT ($(du -h "$OUT" | cut -f1))"
