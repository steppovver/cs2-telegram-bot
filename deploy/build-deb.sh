#!/bin/bash
# Собирает amd64 .deb: бинари + юниты + maintainer-скрипты.
# Использование: ./deploy/build-deb.sh
# Результат: dist/cs2bot_<ver>_amd64.deb
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Версия: <двухчастный-тег>.<коммиты-после-тега>, напр. v0.2.4.
# Тег — последний vX.Y, достижимый из HEAD. Грязное дерево или нет тега — ошибка;
# без git — Version=dev (пакетная версия 0.0.0~dev).
if git rev-parse --git-dir >/dev/null 2>&1; then
  if [ -n "$(git status --porcelain)" ]; then
    echo "ОШИБКА: грязное дерево — закоммить или спрятать изменения перед сборкой" >&2
    exit 1
  fi
  TAG="$(git tag --list --merged HEAD | grep -E '^v[0-9]+\.[0-9]+$' | sort -V | tail -n 1)"
  if [ -z "$TAG" ]; then
    echo "ОШИБКА: нет достижимого тега вида vX.Y — создай его (напр. git tag -a v0.2 -m v0.2)" >&2
    exit 1
  fi
  COUNT="$(git rev-list "$TAG..HEAD" --count)"
  SHORT_SHA="$(git rev-parse --short HEAD)"
  VERSION="$TAG.$COUNT"
  PKG_VERSION="${VERSION#v}"
else
  VERSION="dev"
  PKG_VERSION="0.0.0~dev"
  SHORT_SHA="unknown"
fi
BUILD_DATE="$(date -u +%Y-%m-%d)"
LDFLAGS="-X cs2bot/internal/bot.Version=$VERSION -X cs2bot/internal/bot.BuildDate=$BUILD_DATE -X cs2bot/internal/bot.BuildCommit=$SHORT_SHA"

command -v nfpm >/dev/null || {
  echo "ОШИБКА: nfpm не найден. Установи: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest" >&2
  exit 1
}

rm -rf dist/bin
mkdir -p dist/bin

echo "Сборка бинарей ($VERSION)..."
GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/bin/cs2bot ./cmd/bot
GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o dist/bin/teams_puller ./cmd/teams_puller
install -m 0755 deploy/cs2bot-backup dist/bin/cs2bot-backup

export VERSION="$PKG_VERSION"
DEB="dist/cs2bot_${PKG_VERSION}_amd64.deb"
echo "Упаковка $DEB..."
nfpm pkg --packager deb --config deploy/nfpm.yaml --target "$DEB"

echo "OK: $DEB ($(du -h "$DEB" | cut -f1))"
echo "$DEB"
