package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gopkg.in/telebot.v3"
)

func (app *Application) loggingMiddleware(next telebot.HandlerFunc) telebot.HandlerFunc {
	return func(c telebot.Context) error {
		start := time.Now()
		action := "message"
		if c.Callback() != nil {
			action = "callback: " + c.Callback().Data
		} else if c.Message() != nil {
			action = "text: " + c.Message().Text
		}

		err := next(c)

		slog.Info("Входящий запрос",
			slog.Int64("user_id", c.Sender().ID),
			slog.String("username", c.Sender().Username),
			slog.String("action", action),
			slog.Duration("duration", time.Since(start)),
		)
		return err
	}
}

func (app *Application) handleStart(c telebot.Context) error {
	return c.Send("Привет! Выбери нужное действие в меню ниже:", mainMenu)
}

func (app *Application) handleSubscribe(c telebot.Context) error {
	userID := c.Sender().ID
	subs, err := app.db.GetUserSubscriptions(userID)
	if err != nil {
		slog.Error("Ошибка получения подписок", slog.Int64("user_id", userID), slog.Any("error", err))
	}

	menu := buildTeamsKeyboard("sub_", SupportedTeams, subs)
	return c.Send("Выбери команды для получения уведомлений:", menu)
}

func (app *Application) handleSchedule(c telebot.Context) error {
	userID := c.Sender().ID
	subs, err := app.db.GetUserSubscriptions(userID)
	if err != nil {
		return c.Send("Произошла ошибка при обращении к базе данных.")
	}

	if len(subs) == 0 {
		return c.Send("Вы еще не подписаны ни на одну команду.\nНажмите «🔔 Подписаться на команды».")
	}

	matches, err := app.db.GetUpcomingUserMatches(subs)
	if err != nil {
		return c.Send("Ошибка получения расписания.")
	}

	if len(matches) == 0 {
		return c.Send("Для ваших команд в ближайшее время игр не найдено.")
	}

	var sb strings.Builder
	sb.WriteString("🎮 <b>Предстоящие матчи:</b>\n\n")

	for _, match := range matches {
		timeStr := formatTGTime(match.Time, "dt", "02.01 15:04 UTC")
		teamA, teamB := match.TeamA, match.TeamB
		for _, sub := range subs {
			if strings.EqualFold(match.TeamA, sub) {
				teamA = "<b>" + teamA + "</b>"
			}
			if strings.EqualFold(match.TeamB, sub) {
				teamB = "<b>" + teamB + "</b>"
			}
		}
		sb.WriteString(fmt.Sprintf("⏰ %s | %s vs %s\n", timeStr, teamA, teamB))
	}

	return c.Send(sb.String(), telebot.ModeHTML)
}

func (app *Application) handleToggleSub(c telebot.Context) error {
	payload := c.Callback().Data
	parts := strings.Split(payload, "|")
	if len(parts) != 3 {
		return c.Respond(&telebot.CallbackResponse{Text: "Ошибка формата данных."})
	}

	teamName := strings.ToUpper(parts[2])
	userID := c.Sender().ID

	subs, _ := app.db.GetUserSubscriptions(userID)
	isSubbed := isTeamSubscribed(subs, teamName)

	var toastMsg string
	if isSubbed {
		if err := app.db.Unsubscribe(userID, teamName); err != nil {
			slog.Error("Ошибка при отписке", slog.Int64("user_id", userID), slog.String("team", teamName), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Ошибка при отписке."})
		}
		toastMsg = fmt.Sprintf("Отписка от %s", teamName)
		slog.Info("Пользователь отписался", slog.Int64("user_id", userID), slog.String("team", teamName))
	} else {
		if err := app.db.Subscribe(userID, teamName); err != nil {
			slog.Error("Ошибка при подписке", slog.Int64("user_id", userID), slog.String("team", teamName), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Ошибка при подписке."})
		}
		toastMsg = fmt.Sprintf("Подписка на %s оформлена!", teamName)
		slog.Info("Пользователь подписался", slog.Int64("user_id", userID), slog.String("team", teamName))
	}

	c.Respond(&telebot.CallbackResponse{Text: toastMsg})

	newSubs, _ := app.db.GetUserSubscriptions(userID)
	menu := buildTeamsKeyboard("sub_", SupportedTeams, newSubs)
	return c.Edit("Выбери команды для получения уведомлений:", menu)
}

func isTeamSubscribed(subs []string, team string) bool {
	for _, s := range subs {
		if strings.EqualFold(s, team) {
			return true
		}
	}
	return false
}

func buildTeamsKeyboard(actionPrefix string, teamsToDisplay []TeamInfo, userSubs []string) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	var currentRow []telebot.Btn

	for _, t := range teamsToDisplay {
		payload := actionPrefix + "|" + t.ID + "|" + t.Name
		btnText := t.Name
		if isTeamSubscribed(userSubs, t.Name) {
			btnText = "✅ " + t.Name
		}
		currentRow = append(currentRow, menu.Data(btnText, actionPrefix, payload))

		if len(currentRow) == 2 {
			rows = append(rows, menu.Row(currentRow...))
			currentRow = nil
		}
	}
	if len(currentRow) > 0 {
		rows = append(rows, menu.Row(currentRow...))
	}

	menu.Inline(rows...)
	return menu
}
