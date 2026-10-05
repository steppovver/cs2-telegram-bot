# Проект cs2-telegram-bot

Telegram-бот на Go для отслеживания матчей Counter-Strike 2 (SQLite + PandaScore API).

Подхвачено Cursor / OpenCode / Codex: общие правила лежат в `OPENCODE.md`, Cursor-специфика — в `.cursor/rules/`.

## Стек и архитектура
- **Язык**: Go (актуальная версия из `go.mod`)
- **База данных**: SQLite (драйвер `modernc.org/sqlite`), WAL-режим, один файл `bot.db`
- **API**: PandaScore API
- `cmd/bot/main.go` — точка входа бота
- `cmd/teams_puller/main.go` — утилита синхронизации команд
- `internal/api/` — клиенты внешних API (PandaScore)
- `internal/bot/` — обработчики Telegram (handlers, workers: поллер, напоминания, рассылка, дайджест)
- `internal/domain/` — доменные модели и интерфейсы
- `internal/storage/` — слой работы с базой данных (SQLite)
- `deploy/` — `cs2bot.service`, `backup-sqlite.sh`, `deploy.sh`

## Правила кодогенерации
1. **Обработка ошибок**: явно через `if err != nil`, оборачивание с контекстом (`fmt.Errorf("...: %w", err)`).
2. **Конкурентность**: `context.Context`, без утечек горутин, `defer` для закрытия соединений.
3. **Проверки**: `go vet ./...`, `go build ./cmd/bot`, `go test ./...` перед завершением задачи.
4. **Миграции БД** — только с явного подтверждения.
5. Секреты только из `config.json` / env, не коммитить (`config.json`, `.env` в `.gitignore` и `.cursorignore`).
