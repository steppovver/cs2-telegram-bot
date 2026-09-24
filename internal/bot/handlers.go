package bot

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"cs2bot/internal/domain"

	"gopkg.in/telebot.v3"
)

func (b *Bot) loggingMiddleware(next telebot.HandlerFunc) telebot.HandlerFunc {
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

func (b *Bot) handleStart(c telebot.Context) error {
	return c.Send("Привет! Выбери нужное действие в меню ниже:", b.mainMenu)
}

func (b *Bot) handleSubscribe(c telebot.Context) error {
	userID := c.Sender().ID
	subs, err := b.storage.GetUserSubscriptions(userID)
	if err != nil {
		slog.Error("Ошибка получения подписок", slog.Int64("user_id", userID), slog.Any("error", err))
	}

	// Подтягиваем имена базовых команд из БД
	ids := make([]int, len(b.defaultTeams))
	for i, t := range b.defaultTeams {
		ids[i] = t.ID
	}
	baseTeams, err := b.storage.GetTeamsByIDs(ids)
	if err != nil {
		slog.Error("Ошибка получения базовых команд", slog.Any("error", err))
		baseTeams = nil
	}

	// Собираем маппинг ID -> команда для быстрого поиска
	teamMap := make(map[int]domain.TeamInfo)
	for _, t := range baseTeams {
		teamMap[t.ID] = t
	}
	for _, t := range subs {
		teamMap[t.ID] = t
	}

	// Сначала базовые команды (в порядке конфига), потом пользовательские
	seen := make(map[int]bool)
	var displayTeams []domain.TeamInfo

	for _, t := range b.defaultTeams {
		if info, ok := teamMap[t.ID]; ok {
			displayTeams = append(displayTeams, info)
			seen[t.ID] = true
		}
	}
	for _, t := range subs {
		if !seen[t.ID] {
			displayTeams = append(displayTeams, t)
			seen[t.ID] = true
		}
	}

	menu := b.buildTeamsKeyboard("sub_", displayTeams, subs)
	return c.Send("Выбери команды для получения уведомлений:", menu)
}

func (b *Bot) handleSchedule(c telebot.Context) error {
	userID := c.Sender().ID
	subs, err := b.storage.GetUserSubscriptions(userID)
	if err != nil {
		return c.Send("Произошла ошибка при обращении к базе данных.")
	}

	if len(subs) == 0 {
		return c.Send("Вы еще не подписаны ни на одну команду.\nНажмите «🔔 Подписки на команды».")
	}

	// Загружаем live-матчи из БД
	liveMatches, err := b.storage.GetLiveUserMatches(userID)
	if err != nil {
		slog.Error("Ошибка получения live-матчей", slog.Int64("user_id", userID), slog.Any("error", err))
	}

	// Загружаем предстоящие матчи из БД
	matches, err := b.storage.GetUpcomingUserMatches(userID)
	if err != nil {
		return c.Send("Ошибка получения расписания.")
	}

	if len(liveMatches) == 0 && len(matches) == 0 {
		return c.Send("Для ваших команд в ближайшее время игр не найдено.")
	}

	var sb strings.Builder

	// Сначала live-матчи
	if len(liveMatches) > 0 {
		sb.WriteString("🔴 <b>Сейчас играют:</b>\n\n")
		for _, match := range liveMatches {
			teamA, teamB := match.TeamA, match.TeamB
			for _, sub := range subs {
				if sub.ID == match.TeamAID {
					teamA = "<b>" + teamA + "</b>"
				}
				if sub.ID == match.TeamBID {
					teamB = "<b>" + teamB + "</b>"
				}
			}
			sb.WriteString(fmt.Sprintf("🔴 %s vs %s\n", teamA, teamB))
		}
		sb.WriteString("\n")
	}

	// Затем предстоящие
	if len(matches) > 0 {
		sb.WriteString("🎮 <b>Предстоящие матчи:</b>\n\n")
		for _, match := range matches {
			timeStr := formatTGTime(match.Time, "dt", "02.01 15:04 UTC")
			teamA, teamB := match.TeamA, match.TeamB
			for _, sub := range subs {
				if sub.ID == match.TeamAID {
					teamA = "<b>" + teamA + "</b>"
				}
				if sub.ID == match.TeamBID {
					teamB = "<b>" + teamB + "</b>"
				}
			}
			sb.WriteString(fmt.Sprintf("⏰ %s | %s vs %s\n", timeStr, teamA, teamB))
		}
	}

	return c.Send(sb.String(), telebot.ModeHTML)
}

func (b *Bot) handleSearchPrompt(c telebot.Context) error {
	return c.Send("Введите часть названия команды (например, Falcon):")
}

func (b *Bot) handleTextSearch(c telebot.Context) error {
	query := strings.TrimSpace(c.Message().Text)
	if len(query) < 2 {
		return c.Send("Введите хотя бы 2 символа для поиска.")
	}

	teams, err := b.storage.SearchTeams(query)
	if err != nil || len(teams) == 0 {
		return c.Send("Команды не найдены.", b.mainMenu)
	}

	subs, _ := b.storage.GetUserSubscriptions(c.Sender().ID)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔍 <b>Результаты поиска по \"%s\":</b>\n\n", query))

	var teamsToDisplay []domain.TeamInfo
	for _, t := range teams {
		sb.WriteString(fmt.Sprintf("🛡 <b>%s</b>\n", t.Name))
		if t.Players != "" {
			sb.WriteString(fmt.Sprintf("👥 Игроки: %s\n\n", t.Players))
		} else {
			sb.WriteString("👥 Игроки: нет данных\n\n")
		}

		teamsToDisplay = append(teamsToDisplay, domain.TeamInfo{
			ID:   t.ID,
			Name: t.Name,
		})
	}

	menu := b.buildTeamsKeyboard("sub_", teamsToDisplay, subs)
	return c.Send(sb.String(), telebot.ModeHTML, menu)
}

func (b *Bot) handleToggleSub(c telebot.Context) error {
	payload := c.Callback().Data
	parts := strings.Split(payload, "|")
	if len(parts) != 3 {
		return c.Respond(&telebot.CallbackResponse{Text: "Ошибка формата данных."})
	}

	teamID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return c.Respond(&telebot.CallbackResponse{Text: "Ошибка идентификатора команды."})
	}
	teamName := parts[2]
	userID := c.Sender().ID

	subs, err := b.storage.GetUserSubscriptions(userID)
	if err != nil {
		slog.Error("Ошибка проверки подписок перед изменением", slog.Int64("user_id", userID), slog.Any("error", err))
		return c.Respond(&telebot.CallbackResponse{Text: "Внутренняя ошибка сервера. Попробуйте позже."})
	}

	isSubbed := isTeamSubscribed(subs, int(teamID))

	var toastMsg string
	if isSubbed {
		err = b.storage.Unsubscribe(userID, teamID)
		toastMsg = fmt.Sprintf("Отписка от %s", teamName)
	} else {
		err = b.storage.Subscribe(userID, teamID, teamName)
		toastMsg = fmt.Sprintf("Подписка на %s оформлена!", teamName)
	}

	if err != nil {
		slog.Error("Ошибка изменения подписки в БД", slog.Int64("user_id", userID), slog.Int64("team_id", teamID), slog.Any("error", err))
		return c.Respond(&telebot.CallbackResponse{Text: "Не удалось сохранить изменения."})
	}

	// Отправляем успешный toast-ответ
	_ = c.Respond(&telebot.CallbackResponse{Text: toastMsg})

	markup := c.Message().ReplyMarkup
	if markup != nil {
		for i, row := range markup.InlineKeyboard {
			for j, btn := range row {
				if strings.Contains(btn.Data, payload) {
					if isSubbed {
						markup.InlineKeyboard[i][j].Text = strings.TrimPrefix(btn.Text, "✅ ")
					} else {
						markup.InlineKeyboard[i][j].Text = "✅ " + btn.Text
					}
				}
			}
		}
		return c.Edit(c.Message().Text, markup, telebot.ModeHTML)
	}
	return nil
}

func (b *Bot) buildTeamsKeyboard(actionPrefix string, teamsToDisplay []domain.TeamInfo, userSubs []domain.TeamInfo) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	var currentRow []telebot.Btn

	for _, t := range teamsToDisplay {
		payload := actionPrefix + "|" + strconv.Itoa(t.ID) + "|" + t.Name
		btnText := t.Name
		if isTeamSubscribed(userSubs, t.ID) {
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

func isTeamSubscribed(subs []domain.TeamInfo, teamID int) bool {
	for _, s := range subs {
		if s.ID == teamID {
			return true
		}
	}
	return false
}
