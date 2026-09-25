package bot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cs2bot/internal/domain"

	"gopkg.in/telebot.v3"
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

		var msg string
		timeStr := formatTGTime(match.Time, "dt", "15:04 02.01 UTC")

		if isNew {
			msg = fmt.Sprintf("🆕 <b>Добавлен новый матч!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n⏰ Время: %s",
				match.TeamA, match.TeamB, timeStr)
		} else if match.Status == "running" && statusChanged && oldStatus != "running" {
			msg = fmt.Sprintf("🔴 <b>Матч начался!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n⏰ Время: %s",
				match.TeamA, match.TeamB, timeStr)
		} else if teamsChanged {
			timeText := fmt.Sprintf("⏰ Время: %s", timeStr)
			if timeChanged {
				oldTimeStr := formatTGTime(oldTime, "dt", "15:04 02.01 UTC")
				timeText = fmt.Sprintf("<s>Время: %s</s>\n⏰ Новое: %s", oldTimeStr, timeStr)
			}
			msg = fmt.Sprintf("🔄 <b>Определился соперник!</b>\n\n<s>%s vs %s</s>\n🛡 <b>%s</b> vs <b>%s</b>\n%s",
				oldTeamA, oldTeamB, match.TeamA, match.TeamB, timeText)
		} else if timeChanged {
			oldTimeStr := formatTGTime(oldTime, "dt", "15:04 02.01 UTC")
			msg = fmt.Sprintf("⚠️ <b>Время матча изменено!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n<s>Старое время: %s</s>\n⏰ Новое время: %s",
				match.TeamA, match.TeamB, oldTimeStr, timeStr)
		}

		if msg == "" {
			continue
		}
		if !b.broadcastToFans(ctx, match, msg) {
			slog.Warn("Уведомление не поставлено в очередь (переполнение или отмена)",
				slog.Int("match_id", match.ID))
		}
	}

	b.storage.CleanStaleRunningMatches(apiMatchIDs)
	b.storage.CleanOldMatches()
}

func (b *Bot) StartMatchReminders(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Воркер напоминаний завершил работу")
			return
		case <-ticker.C:
			b.runRemindersCycle(ctx)
		}
	}
}

func (b *Bot) runRemindersCycle(ctx context.Context) {
	matches, err := b.storage.GetMatchesForReminder()
	if err != nil {
		slog.Error("Ошибка получения матчей для напоминаний", slog.Any("error", err))
		return
	}

	for _, match := range matches {
		if match.TeamA == "TBD" || match.TeamB == "TBD" {
			continue
		}

		timeStr := formatTGTime(match.Time, "t", "15:04 UTC")
		msg := fmt.Sprintf("🔥 <b>Матч начнется с минуты на минуту!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\nНачало в %s",
			match.TeamA, match.TeamB, timeStr)

		if !b.broadcastToFans(ctx, match, msg) {
			slog.Warn("Напоминание не поставлено в очередь, повтор на следующем цикле",
				slog.Int("match_id", match.ID))
			continue
		}

		if err := b.storage.MarkMatchAsNotified(match.ID); err != nil {
			slog.Error("Ошибка отметки матча как уведомленного", slog.Int("match_id", match.ID), slog.Any("error", err))
			continue
		}
	}
}

func (b *Bot) StartBroadcaster(ctx context.Context) {
	// Telegram разрешает 30 сообщений в секунду (глобально).
	// Ограничиваем до 25 (тик каждые 40 мс) для надежности.
	limiter := time.NewTicker(40 * time.Millisecond)
	defer limiter.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Воркер рассылок завершил работу")
			return
		case task := <-b.broadcastCh:
			<-limiter.C // Ждем разрешения от тикера перед отправкой
			if _, err := b.telebot.Send(telebot.ChatID(task.UserID), task.Text, telebot.ModeHTML); err != nil {
				slog.Warn("Ошибка отправки", slog.Int64("user_id", task.UserID), slog.Any("error", err))
			}
		}
	}
}

func (b *Bot) broadcastToFans(ctx context.Context, match domain.Match, msg string) bool {
	users, err := b.storage.GetUsersByTeamIDs(match.TeamAID, match.TeamBID)
	if err != nil {
		slog.Error("Ошибка получения подписчиков матча",
			slog.String("team_a", match.TeamA),
			slog.String("team_b", match.TeamB),
			slog.Any("error", err))
		return false
	}

	if len(users) == 0 {
		return true
	}

	slog.Info("Добавление в очередь рассылки",
		slog.String("match", fmt.Sprintf("%s vs %s", match.TeamA, match.TeamB)),
		slog.Int("recipients", len(users)))

	dropped := 0
	for _, userID := range users {
		// Быстрая проверка отмены без блокировки поллера.
		select {
		case <-ctx.Done():
			return false
		default:
		}

		select {
		case b.broadcastCh <- BroadcastTask{UserID: userID, Text: msg}:
		default:
			dropped++
		}
	}

	if dropped > 0 {
		slog.Warn("Очередь рассылки переполнена, часть уведомлений отброшена",
			slog.String("match", fmt.Sprintf("%s vs %s", match.TeamA, match.TeamB)),
			slog.Int("dropped", dropped),
			slog.Int("recipients", len(users)),
			slog.Int("queue_len", len(b.broadcastCh)),
		)
		return false
	}
	return true
}

func formatTGTime(t time.Time, tgFormat, fallbackFormat string) string {
	return fmt.Sprintf(`<tg-time unix="%d" format="%s">%s</tg-time>`,
		t.Unix(), tgFormat, t.UTC().Format(fallbackFormat))
}
