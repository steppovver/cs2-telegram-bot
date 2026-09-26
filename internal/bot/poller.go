package bot

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"time"
)

func (b *Bot) StartPoller(ctx context.Context) {
	b.runPollerCycle(ctx)

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Воркер опрашивания матчей завершил работу")
			return
		case <-ticker.C:
			b.runPollerCycle(ctx)
		}
	}
}

func (b *Bot) runPollerCycle(ctx context.Context) {
	slog.Debug("Запуск цикла обновления матчей")

	idsToFetch, err := b.storage.GetSubscribedTeamIDs()
	if err != nil {
		slog.Error("Ошибка получения ID команд из БД", slog.Any("error", err))
		return
	}
	if len(idsToFetch) == 0 {
		return
	}

	// Обновляем предстоящие матчи
	matches, err := b.panda.FetchMatchesByTeamIDs(ctx, idsToFetch)
	if err != nil {
		slog.Error("Ошибка запроса матчей из API", slog.Any("error", err))
		return
	}

	// Собираем ID матчей из ответа API
	apiMatchIDs := make(map[int]bool, len(matches))
	for _, m := range matches {
		apiMatchIDs[m.ID] = true
	}

	for _, match := range matches {
		isNew, timeChanged, teamsChanged, statusChanged, oldTime, oldTeamA, oldTeamB, oldStatus, err := b.storage.ProcessMatch(match)
		if err != nil {
			slog.Error("Ошибка сохранения матча", slog.Int("match_id", match.ID), slog.Any("error", err))
			continue
		}

		if (!isNew && !timeChanged && !teamsChanged && !statusChanged) || match.TeamA == "TBD" || match.TeamB == "TBD" {
			continue
		}

		// Матчи без времени начала (begin_at=null в API) не рассылаем:
		// иначе спамим датой 01.01.0001, а CleanOldMatches все равно их сотрет.
		if match.Time.IsZero() {
			continue
		}

		var build func(off int) string
		escA, escB := html.EscapeString(match.TeamA), html.EscapeString(match.TeamB)
		escOldA, escOldB := html.EscapeString(oldTeamA), html.EscapeString(oldTeamB)

		if isNew {
			build = func(off int) string {
				timeStr := formatTGTime(match.Time, "dt", "15:04 02.01", off)
				return fmt.Sprintf("🆕 <b>Добавлен новый матч!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n⏰ Время: %s",
					escA, escB, timeStr)
			}
		} else if match.Status == "running" && statusChanged && oldStatus != "running" {
			build = func(off int) string {
				timeStr := formatTGTime(match.Time, "dt", "15:04 02.01", off)
				return fmt.Sprintf("🔴 <b>Матч начался!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n⏰ Время: %s",
					escA, escB, timeStr)
			}
		} else if teamsChanged {
			build = func(off int) string {
				timeStr := formatTGTime(match.Time, "dt", "15:04 02.01", off)
				timeText := fmt.Sprintf("⏰ Время: %s", timeStr)
				if timeChanged {
					oldTimeStr := formatTGTime(oldTime, "dt", "15:04 02.01", off)
					timeText = fmt.Sprintf("<s>Время: %s</s>\n⏰ Новое: %s", oldTimeStr, timeStr)
				}
				return fmt.Sprintf("🔄 <b>Определился соперник!</b>\n\n<s>%s vs %s</s>\n🛡 <b>%s</b> vs <b>%s</b>\n%s",
					escOldA, escOldB, escA, escB, timeText)
			}
		} else if timeChanged {
			build = func(off int) string {
				timeStr := formatTGTime(match.Time, "dt", "15:04 02.01", off)
				oldTimeStr := formatTGTime(oldTime, "dt", "15:04 02.01", off)
				return fmt.Sprintf("⚠️ <b>Время матча изменено!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n<s>Старое время: %s</s>\n⏰ Новое время: %s",
					escA, escB, oldTimeStr, timeStr)
			}
		}

		if build == nil {
			continue
		}
		if !b.broadcastToFans(ctx, match, build) {
			slog.Warn("Уведомление не поставлено в очередь (переполнение или отмена)",
				slog.Int("match_id", match.ID))
		}
	}

	b.storage.CleanStaleRunningMatches(apiMatchIDs)
	b.storage.CleanOldMatches()
}
