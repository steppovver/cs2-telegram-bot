package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cs2bot/internal/api"

	"gopkg.in/telebot.v3"
)

func (app *Application) broadcastToFans(teamA, teamB, msg string) {
	usersA, err := app.db.GetUsersByTeam(teamA)
	if err != nil {
		slog.Error("Не удалось получить подписчиков", slog.String("team", teamA), slog.Any("error", err))
	}
	usersB, err := app.db.GetUsersByTeam(teamB)
	if err != nil {
		slog.Error("Не удалось получить подписчиков", slog.String("team", teamB), slog.Any("error", err))
	}

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

	for userID := range uniqueUsers {
		if _, err := app.bot.Send(telebot.ChatID(userID), msg, telebot.ModeHTML); err != nil {
			slog.Warn("Ошибка отправки", slog.Int64("user_id", userID), slog.Any("error", err))
		}
	}
}

func (app *Application) startPoller() {
	updateRoutine := func() {
		slog.Debug("Запуск цикла обновления подписок")
		subscribedTeams, err := app.db.GetAllSubscribedTeams()
		if err != nil {
			slog.Error("Ошибка получения подписанных команд", slog.Any("error", err))
			return
		}
		if len(subscribedTeams) == 0 {
			return
		}

		var idsToFetch []string
		for _, teamName := range subscribedTeams {
			for _, t := range SupportedTeams {
				if strings.EqualFold(t.Name, teamName) {
					idsToFetch = append(idsToFetch, t.ID)
					break
				}
			}
		}

		if len(idsToFetch) == 0 {
			return
		}

		matches, err := api.FetchMatchesByTeamIDs(app.pandaToken, idsToFetch)
		if err != nil {
			slog.Error("Ошибка запроса матчей", slog.Any("error", err))
			return
		}

		for _, match := range matches {
			isNew, timeChanged, teamsChanged, oldTime, oldTeamA, oldTeamB, err := app.db.ProcessMatch(match)
			if err != nil {
				slog.Error("Ошибка сохранения матча", slog.Int("match_id", match.ID), slog.Any("error", err))
				continue
			}

			if !isNew && !timeChanged && !teamsChanged {
				continue
			}

			if match.TeamA == "TBD" || match.TeamB == "TBD" {
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

			app.broadcastToFans(match.TeamA, match.TeamB, msg)
		}

		app.db.CleanOldMatches()
	}

	updateRoutine()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		updateRoutine()
	}
}

func (app *Application) startMatchReminders() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		matches, err := app.db.GetMatchesForReminder()
		if err != nil {
			slog.Error("Ошибка получения матчей для напоминаний", slog.Any("error", err))
			continue
		}

		for _, match := range matches {
			if match.TeamA == "TBD" || match.TeamB == "TBD" {
				continue
			}

			if err := app.db.MarkMatchAsNotified(match.ID); err != nil {
				slog.Error("Ошибка отметки матча как уведомленного", slog.Int("match_id", match.ID), slog.Any("error", err))
				continue
			}

			timeStr := formatTGTime(match.Time, "t", "15:04 UTC")
			msg := fmt.Sprintf("🔥 <b>Матч начнется с минуты на минуту!</b>\n\n🛡 <b>%s</b> vs <b>%s</b>\nНачало в %s",
				match.TeamA, match.TeamB, timeStr)

			app.broadcastToFans(match.TeamA, match.TeamB, msg)
		}
	}
}

func formatTGTime(t time.Time, tgFormat, fallbackFormat string) string {
	return fmt.Sprintf(`<tg-time unix="%d" format="%s">%s</tg-time>`,
		t.Unix(), tgFormat, t.UTC().Format(fallbackFormat))
}
