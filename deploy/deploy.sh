#!/bin/bash
set -ex
# Корень проекта — родитель каталога скрипта. Все пути ниже относительно него,
# поэтому скрипт работает из любого места: ./deploy/deploy.sh или bash deploy/deploy.sh
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Деплойные параметры (куда катить) — из .env, IP и порт в коде не храним.
if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

DEPLOY_USER="${DEPLOY_USER:-root}"
DEPLOY_HOST="${DEPLOY_HOST:?DEPLOY_HOST не задан — добавь его в .env (см. .env.example)}"
DEPLOY_PORT="${DEPLOY_PORT:-22}"
DEPLOY_DIR="${DEPLOY_DIR:-/opt/cs2bot}"

SSH="ssh -p $DEPLOY_PORT $DEPLOY_USER@$DEPLOY_HOST"
SCP="scp -P $DEPLOY_PORT"

GOOS=linux GOARCH=amd64 go build -o cs2bot ./cmd/bot
GOOS=linux GOARCH=amd64 go build -o teams_puller ./cmd/teams_puller

$SSH "systemctl stop cs2bot"
# Бэкап БД перед деплоем + копия предыдущего бинаря для отката.
$SSH "mkdir -p $DEPLOY_DIR/backups && sqlite3 $DEPLOY_DIR/bot.db \".backup '$DEPLOY_DIR/backups/bot-predeploy-\$(date +%F-%H%M).db'\" && cp -f $DEPLOY_DIR/cs2bot $DEPLOY_DIR/cs2bot.prev"
$SCP cs2bot "$DEPLOY_USER@$DEPLOY_HOST:$DEPLOY_DIR/"
$SCP teams_puller "$DEPLOY_USER@$DEPLOY_HOST:$DEPLOY_DIR/"
$SCP deploy/backup-sqlite.sh "$DEPLOY_USER@$DEPLOY_HOST:$DEPLOY_DIR/"
# Сервис работает от cs2bot, а копируем от root — чиним владельца,
# иначе бот не сможет писать в bot.db. Конфиг только для cs2bot.
$SSH "chown -R cs2bot:cs2bot $DEPLOY_DIR && chmod 600 $DEPLOY_DIR/config.json && chmod +x $DEPLOY_DIR/backup-sqlite.sh"


# Синхронизация юнита: заливаем в /etc/systemd только если изменился,
# обычные деплои systemd не трогают.
LOCAL_SUM="$(sha256sum deploy/cs2bot.service | cut -d' ' -f1)"
REMOTE_SUM="$($SSH "sha256sum /etc/systemd/system/cs2bot.service 2>/dev/null | cut -d' ' -f1" || true)"
if [ "$LOCAL_SUM" != "$REMOTE_SUM" ]; then
  echo "Юнит изменился, обновляем..."
  $SCP deploy/cs2bot.service "$DEPLOY_USER@$DEPLOY_HOST:/etc/systemd/system/cs2bot.service"
  $SSH "systemctl daemon-reload"
else
  echo "Юнит без изменений, пропускаем."
fi
$SSH "systemctl restart cs2bot && systemctl is-active cs2bot"
