# Проект cs2-telegram-bot

Telegram-бот на Go для отслеживания матчей Counter-Strike 2 (SQLite + PandaScore API).

Единственный свод правил для Cursor и OpenCode — этот файл. Оба читают его сами, других файлов правил не заводить.

## Не затирать чужую работу

Cursor и OpenCode правят один и тот же репозиторий и обязаны следовать этому файлу, а не вкусу конкретной модели.

1. Перед правкой прочитай файл и незакоммиченный diff. Чужие свежие изменения сохраняй и встраивайся в них, не откатывай и не пиши файл заново.
2. Меняй только то, что нужно для текущей задачи. Не переформатируй, не переименовывай и не рефактори остальное.
3. Если код уже соблюдает правила ниже — оставь его как есть.
4. Проверки и запреты бери только из раздела «Правила кодогенерации». Не подменяй их другим набором команд.
5. Коммит — только когда пользователь прямо попросил.
6. Пользовательский и деплойный `README.md` обновляй в той же задаче, если меняется поведение бота, поля `config.json`, команды, деплой или архитектура, о которых там написано. Не оставляй README устаревшим «на потом».

## Терминал

Без спроса можно только чтение git: `git status`, `git diff`, `git log`, `git show`. В Cursor это `.cursor/permissions.json`, в OpenCode — `permissions` в `opencode.json`. Любая другая команда идёт через обычный запрос инструмента. Обходить его нельзя.

## Стек

- **Язык**: Go `1.25.0` (см. `go.mod`)
- **Telegram**: `gopkg.in/telebot.v3`, long polling или webhook (выбор через `telegram_mode`), только **один инстанс** на токен
- **Логи**: `github.com/lmittmann/tint` через `log/slog`
- **База данных**: SQLite, pure-Go драйвер `modernc.org/sqlite`, WAL-режим, один файл `bot.db`
- **API**: PandaScore API (REST, токен `pandascore_token`)

## Архитектура

- `cmd/bot/main.go` — точка входа: конфиг, логгер, SQLite, PandaScore-клиент, telebot, резолверы, запуск 7 воркеров + `botApp.Start()` в `sync.WaitGroup` с graceful shutdown по `SIGINT/SIGTERM`
- `cmd/teams_puller/main.go` — отдельный бинарь справочника команд: тянет команды с игроками из PandaScore в те же таблицы `teams`/`players`; на сервере — systemd timer `cs2bot-teams-puller.timer` раз в сутки
- `internal/api/` — клиент PandaScore: `client.go` (чанки, ретраи, пагинация, учет лимита запросов), `types.go`, `mapping.go`
- `internal/bot/` — Telegram-слой:
  - `handlers.go`, `digest_handler.go`, `score_handler.go`, `admin.go` — хендлеры и админка (`/admin`, `/version`)
 - `tournament_subs.go` — меню подписок на турниры (только плей-офф), режим по умолчанию и кнопка показать/скрыть турнирные матчи под расписанием и счетом
  - `poller.go`, `pace.go` — опрос предстоящих/live матчей с динамическим темпом; `finished_poller.go` — добор завершенных для кнопки `📊`
  - `reminders.go` — напоминания за 5 минут; `digest_worker.go` — ежедневный дайджест
  - `broadcaster.go` — очередь рассылки (~25 msg/sec, чистка заблокировавших, пауза по FloodWait)
  - `streams.go`, `tournaments.go`, `format.go`, `matches_message.go`, `tz.go` — рендер расписания/счета, часовые пояса
  - `hltv.go`, `youtube.go` — подключение резолверов к рендеру
  - `telegram_poller.go` — выбор polling/webhook и свой `WebhookPoller` (вместо `telebot.Webhook`, который паникует на остановке)
- `internal/hltv/` — поиск и резолв точных ссылок на матчи HLTV (`resolver.go`, `search.go` с провайдерами `ddg`/`searxng`/`none`, `match.go`); запуск за 12 часов до старта
- `internal/youtube/` — oEmbed-резолвер имен YouTube-каналов (`resolver.go`, `oembed.go`, `videoid.go`), окно `[-6h, +48h]`, кеш в `stream_channels`
- `internal/config/` — `config.go`: загрузка и нормализация `config.json` (дефолты: `poll 60s` как fallback живого поллера, `finished_poll 120s`, `api_rate_limit 1000`, `search_provider ddg`, `telegram_mode polling`, `webhook_listen 127.0.0.1:18090`, `digest_preset_hours [9,10,11,12]`, `max_streams 0=all, cap 10`)
- `internal/domain/` — `models.go`: `Match`, `MatchChange`, `MatchStream`, `TeamInfo`, `DigestSettings` и др.
- `internal/storage/` — SQLite: `sqlite.go` (схема, миграции), `matches.go`, `subscriptions.go`, `settings.go`, `streams.go` (кеш `stream_channels`), `tournament_subs.go` (`tournament_teams`, `tournament_subscriptions`, чистка закончившихся турниров)
- `deploy/` — упаковка `.deb` (`nfpm.yaml`, `build-deb.sh`, `debian/{preinst,postinst,prerm,postrm}`), systemd-юниты (`cs2bot.service`, `cs2bot-teams-puller.{service,timer}`, `cs2bot-backup.{service,timer}`), `cs2bot-backup` (онлайн-бэкап SQLite), `deploy.sh` (сборка + `apt install` на сервер), `Caddyfile.cs2bot.example` (webhook за Caddy)
- Прочее: `api-examples/` (примеры ответов PandaScore: групповой матч и финал плей-офф), `config_example.json`, `.env.example` (переменные для `deploy.sh`)

### Раскладка пакета на сервере

- Бинари: `/opt/cs2bot/bin/{cs2bot,teams_puller,cs2bot-backup}`, симлинки в `/usr/bin`
- Конфиг: `/etc/cs2bot/config.json` (не входит в пакет, `postinst` создаёт из примера только если файла нет)
- Данные: `/var/lib/cs2bot/bot.db`, бэкапы в `/var/lib/cs2bot/backups/`
- Примеры: `/usr/share/doc/cs2bot/examples/`

## Воркеры (`cmd/bot/main.go`)

Опрос матчей (динамический темп 50% лимита PandaScore в час; выключается при остатке ≤ 40%, включается при > 50%; иначе и без заголовка — `poll_interval_seconds`, 15–3600) → добор завершенных (`finished_poll_interval_seconds`, 60–86400) → HLTV-резолвер → YouTube-резолвер (каждые 5 мин) → напоминания (каждую минуту, окно 5 мин) → рассылка → дайджест (каждую минуту). Расписание, напоминания, дайджест и события поллера читают имена стримов только из кеша БД.

## Правила кодогенерации

1. **Ошибки**: только явно через `if err != nil`, оборачивание с контекстом (`fmt.Errorf("...: %w", err)`). Не глотать.
2. **Конкурентность**: `context.Context` во всех воркерах и I/O, `defer` для закрытия соединений/транзакций, без утечек горутин (`WaitGroup` в `main`).
3. **Проверки**: `go vet ./...`, `go build ./cmd/bot`, `go build ./cmd/teams_puller`, `go test ./...` перед завершением задачи. При падении — исправить и повторить, финальный ответ только после зеленой сборки.
4. **БД**: схему и миграции не менять без явного подтверждения пользователя.
5. **Время**: везде с учетом часового пояса пользователя (дайджест хранит UTC offset).
6. **Секреты**: `telegram_token`, `pandascore_token`, `webhook_secret` только из `config.json` / env, никогда в код, логи и git (`config.json`, `.env` в `.gitignore` и `.cursorignore`; БД `*.db*` и бинари `/cs2bot`, `/teams_puller`, `/bot` тоже игнорируются).
7. **README**: при изменении пользовательского поведения, конфига, деплоя или публичной архитектуры — обновить соответствующие разделы `README.md` в том же изменении.
