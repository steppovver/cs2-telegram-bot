package bot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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
	slog.Debug("Запуск цикла обновления подписок")

	// Получаем список названий всех команд, на которые подписаны пользователи
	subscribedTeams, err := b.storage.GetAllSubscribedTeams()
	if err != nil || len(subscribedTeams) == 0 {
		return
	}

	// Делаем ОДИН быстрый запрос к SQLite для ВСЕХ команд
	dbIDs, err := b.storage.GetTeamIDsByNames(subscribedTeams)
	if err != nil {
		slog.Error("Ошибка получения ID команд из БД", slog.Any("error", err))
		return
	}

	// Собираем слайс ID (dbIDs у нас возвращает map[string]string)
	var idsToFetch []string
	for _, id := range dbIDs {
		idsToFetch = append(idsToFetch, id)
	}

	if len(idsToFetch) == 0 {
		return
	}

	// Отправляем ID в PandaScore API
	matches, err := b.panda.FetchMatchesByTeamIDs(ctx, idsToFetch)
	if err != nil {
		slog.Error("Ошибка запроса матчей из API", slog.Any("error", err))
		return
	}

	for _, match := range matches {
		isNew, timeChanged, teamsChanged, oldTime, oldTeamA, oldTeamB, err := b.storage.ProcessMatch(match)
		if err != nil {
			slog.Error("Ошибка сохранения матча", slog.Int("match_id", match.ID), slog.Any("error", err))
			continue
		}

		if (!isNew && !timeChanged && !teamsChanged) || match.TeamA == "TBD" || match.TeamB == "TBD" {
			continue
		}

		var msg string
		timeStr := formatTGTime(match.Time, "dt", "15:04 02.01 UTC")

		if isNew {
			msg = fmt.Sprintf("🆕 <b>Добавлен новый матч!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\n⏰ Время: %s",
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

		b.broadcastToFans(ctx, match.TeamA, match.TeamB, msg)
	}

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

		if err := b.storage.MarkMatchAsNotified(match.ID); err != nil {
			slog.Error("Ошибка отметки матча как уведомленного", slog.Int("match_id", match.ID), slog.Any("error", err))
			continue
		}

		timeStr := formatTGTime(match.Time, "t", "15:04 UTC")
		msg := fmt.Sprintf("🔥 <b>Матч начнется с минуты на минуту!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\nНачало в %s",
			match.TeamA, match.TeamB, timeStr)

		b.broadcastToFans(ctx, match.TeamA, match.TeamB, msg)
	}
}

func (b *Bot) broadcastToFans(ctx context.Context, teamA, teamB, msg string) {
	usersA, _ := b.storage.GetUsersByTeam(teamA)
	usersB, _ := b.storage.GetUsersByTeam(teamB)

	uniqueUsers := make(map[int64]struct{})
	for _, u := range append(usersA, usersB...) {
		uniqueUsers[u] = struct{}{}
	}

	if len(uniqueUsers) == 0 {
		return
	}

	slog.Info("Рассылка уведомления",
		slog.String("match", fmt.Sprintf("%s vs %s", teamA, teamB)),
		slog.Int("recipients", len(uniqueUsers)))

	limiter := time.NewTicker(35 * time.Millisecond) // ~28 сообщений/сек для защиты от лимитов Telegram
	defer limiter.Stop()

	for userID := range uniqueUsers {
		select {
		case <-ctx.Done():
			slog.Warn("Рассылка прервана сигналом завершения")
			return
		case <-limiter.C:
			if _, err := b.telebot.Send(telebot.ChatID(userID), msg, telebot.ModeHTML); err != nil {
				slog.Warn("Ошибка отправки", slog.Int64("user_id", userID), slog.Any("error", err))
			}
		}
	}
}

func formatTGTime(t time.Time, tgFormat, fallbackFormat string) string {
	return fmt.Sprintf(`<tg-time unix="%d" format="%s">%s</tg-time>`,
		t.Unix(), tgFormat, t.UTC().Format(fallbackFormat))
}
