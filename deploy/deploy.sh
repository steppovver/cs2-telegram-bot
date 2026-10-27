#!/bin/bash
set -ex -o pipefail
# Собирает .deb и ставит его на сервер через apt.
# Корень проекта — родитель каталога скрипта.
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

DEPLOY_USER="${DEPLOY_USER:-root}"
DEPLOY_HOST="${DEPLOY_HOST:?DEPLOY_HOST не задан — добавь его в .env (см. .env.example)}"
DEPLOY_PORT="${DEPLOY_PORT:-22}"
DEB_CACHE="${DEB_CACHE:-/var/cache/cs2bot-debs}"
DEB_KEEP="${DEB_KEEP:-3}"

SSH="ssh -p $DEPLOY_PORT $DEPLOY_USER@$DEPLOY_HOST"
SCP="scp -P $DEPLOY_PORT"

# Сборка пакета (версия/ldflags — внутри build-deb.sh).
DEB_PATH="$(bash deploy/build-deb.sh | tail -n 1)"
if [ ! -f "$DEB_PATH" ]; then
  echo "ОШИБКА: build-deb.sh не вернул путь к пакету (получено: '$DEB_PATH')" >&2
  exit 1
fi
DEB_NAME="$(basename "$DEB_PATH")"

$SSH "mkdir -p $DEB_CACHE"
$SCP "$DEB_PATH" "$DEPLOY_USER@$DEPLOY_HOST:$DEB_CACHE/$DEB_NAME"

# apt install локального .deb: зависимости, preinst/postinst, юниты.
$SSH "apt-get install -y $DEB_CACHE/$DEB_NAME"

# Храним DEB_KEEP последних пакетов для отката.
$SSH "cd $DEB_CACHE && ls -1t cs2bot_*.deb 2>/dev/null | tail -n +$((DEB_KEEP + 1)) | xargs -r rm -f"

$SSH "systemctl is-active cs2bot"
echo "OK: установлен $DEB_NAME"
