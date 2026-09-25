package bot

import (
	"fmt"
	"html"
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

		var userID int64
		var username string
		if s := c.Sender(); s != nil {
			userID = s.ID
			username = s.Username
		}

		slog.Info("Входящий запрос",
			slog.Int64("user_id", userID),
			slog.String("username", username),
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
	if c.Sender() == nil {
		return nil
	}
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
	if c.Sender() == nil {
		return nil
	}
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
			teamA, teamB := html.EscapeString(match.TeamA), html.EscapeString(match.TeamB)
			for _, sub := range subs {
				if sub.ID == match.TeamAID {
					teamA = "<b>" + teamA + "</b>"
				}
				if sub.ID == match.TeamBID {
					teamB = "<b>" + teamB + "</b>"
				}
			}
			sb.WriteString(fmt.Sprintf("%s vs %s\n", teamA, teamB))
		}
		sb.WriteString("\n")
	}

	// Разделяем предстоящие на ближайшие (24ч) и отдалённые
	oneDayLater := time.Now().Add(24 * time.Hour)
	var upcoming []domain.Match
	var further []domain.Match
	for _, match := range matches {
		if match.Time.Before(oneDayLater) {
			upcoming = append(upcoming, match)
		} else {
			further = append(further, match)
		}
	}

	// Ближайшие матчи
	if len(upcoming) > 0 {
		sb.WriteString("⚡ <b>Ближайшие матчи:</b>\n\n")
		for _, match := range upcoming {
			timeStr := formatTGTime(match.Time, "dt", "02.01 15:04 UTC")
			teamA, teamB := html.EscapeString(match.TeamA), html.EscapeString(match.TeamB)
			for _, sub := range subs {
				if sub.ID == match.TeamAID {
					teamA = "<b>" + teamA + "</b>"
				}
				if sub.ID == match.TeamBID {
					teamB = "<b>" + teamB + "</b>"
				}
			}
			sb.WriteString(fmt.Sprintf("%s | %s vs %s\n", timeStr, teamA, teamB))
		}
		sb.WriteString("\n")
	}

	// Отдалённые матчи
	if len(further) > 0 {
		sb.WriteString("📅 <b>Предстоящие матчи:</b>\n\n")
		for _, match := range further {
			timeStr := formatTGTime(match.Time, "dt", "02.01 15:04 UTC")
			teamA, teamB := html.EscapeString(match.TeamA), html.EscapeString(match.TeamB)
			for _, sub := range subs {
				if sub.ID == match.TeamAID {
					teamA = "<b>" + teamA + "</b>"
				}
				if sub.ID == match.TeamBID {
					teamB = "<b>" + teamB + "</b>"
				}
			}
			sb.WriteString(fmt.Sprintf("%s | %s vs %s\n", timeStr, teamA, teamB))
		}
		sb.WriteString("\n")
	}

	return b.sendChunked(c, sb.String())
}

func (b *Bot) handleSearchPrompt(c telebot.Context) error {
	return c.Send("Введите часть названия команды (например, Falcon):")
}

func (b *Bot) handleTextSearch(c telebot.Context) error {
	if c.Message() == nil || c.Sender() == nil {
		return nil
	}
	query := strings.TrimSpace(c.Message().Text)
	if len([]rune(query)) < 2 {
		return c.Send("Введите хотя бы 2 символа для поиска.")
	}

	teams, err := b.storage.SearchTeams(query)
	if err != nil || len(teams) == 0 {
		return c.Send("Команды не найдены.", b.mainMenu)
	}

	subs, _ := b.storage.GetUserSubscriptions(c.Sender().ID)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🔍 <b>Результаты поиска по \"%s\":</b>\n\n", html.EscapeString(query)))

	var teamsToDisplay []domain.TeamInfo
	for _, t := range teams {
		sb.WriteString(fmt.Sprintf("🛡 <b>%s</b>\n", html.EscapeString(t.Name)))
		if t.Players != "" {
			sb.WriteString(fmt.Sprintf("👥 Игроки: %s\n\n", html.EscapeString(t.Players)))
		} else {
			sb.WriteString("👥 Игроки: нет данных\n\n")
		}

		teamsToDisplay = append(teamsToDisplay, domain.TeamInfo{
			ID:   t.ID,
			Name: t.Name,
		})
	}

	menu := b.buildTeamsKeyboard("sub_", teamsToDisplay, subs)
	return b.sendChunked(c, sb.String(), menu)
}

func (b *Bot) handleToggleSub(c telebot.Context) error {
	if c.Callback() == nil || c.Sender() == nil {
		return nil
	}
	teamID, ok := parseTeamIDFromCallback(c.Callback().Data)
	if !ok {
		return c.Respond(&telebot.CallbackResponse{Text: "Ошибка идентификатора команды."})
	}
	userID := c.Sender().ID

	subs, err := b.storage.GetUserSubscriptions(userID)
	if err != nil {
		slog.Error("Ошибка проверки подписок перед изменением", slog.Int64("user_id", userID), slog.Any("error", err))
		return c.Respond(&telebot.CallbackResponse{Text: "Внутренняя ошибка сервера. Попробуйте позже."})
	}

	isSubbed := isTeamSubscribed(subs, int(teamID))

	teamName := teamDisplayName(subs, int(teamID))
	if teams, err := b.storage.GetTeamsByIDs([]int{int(teamID)}); err == nil && len(teams) > 0 && teams[0].Name != "" {
		teamName = teams[0].Name
	}
	if teamName == "" {
		teamName = fmt.Sprintf("команда %d", teamID)
	}

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

	if c.Message() == nil {
		return nil
	}
	markup := c.Message().ReplyMarkup
	if markup != nil {
		for i, row := range markup.InlineKeyboard {
			for j, btn := range row {
				btnTeamID, ok := parseTeamIDFromButton(btn.Data)
				if !ok || btnTeamID != teamID {
					continue
				}
				if isSubbed {
					markup.InlineKeyboard[i][j].Text = strings.TrimPrefix(btn.Text, "✅ ")
				} else {
					markup.InlineKeyboard[i][j].Text = "✅ " + btn.Text
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
		payload := strconv.Itoa(t.ID)
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

// parseTeamIDFromCallback разбирает payload колбэка после маршрутизации telebot.
// Новый формат: "3210". Легаси: "sub_|3210|Name" / "sub_|3210".
func parseTeamIDFromCallback(data string) (int64, bool) {
	data = strings.TrimSpace(data)
	if data == "" {
		return 0, false
	}
	if !strings.Contains(data, "|") {
		id, err := strconv.ParseInt(data, 10, 64)
		if err != nil || id <= 0 {
			return 0, false
		}
		return id, true
	}
	parts := strings.SplitN(data, "|", 3)
	if len(parts) >= 2 && parts[0] == "sub_" {
		id, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil || id <= 0 {
			return 0, false
		}
		return id, true
	}
	for _, p := range parts {
		if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil && id > 0 {
			return id, true
		}
	}
	return 0, false
}

// parseTeamIDFromButton извлекает team_id из сырого callback_data кнопки
// (проводной формат "\fsub_|<inner>"). Совместим со старыми сообщениями.
func parseTeamIDFromButton(btnData string) (int64, bool) {
	s := strings.TrimPrefix(btnData, "\f")
	s = strings.TrimPrefix(s, "sub_|")
	return parseTeamIDFromCallback(s)
}

func teamDisplayName(subs []domain.TeamInfo, teamID int) string {
	for _, s := range subs {
		if s.ID == teamID {
			return s.Name
		}
	}
	return ""
}

// tgChunkLimit — запас под лимит Telegram в 4096 символов на сообщение.
const tgChunkLimit = 3500

// sendChunked отправляет длинный HTML-текст кусками по строкам.
// Дополнительные opts (например, inline-меню) цепляются к последнему куску.
func (b *Bot) sendChunked(c telebot.Context, text string, opts ...interface{}) error {
	htmlMode := []interface{}{telebot.ModeHTML}
	if len(text) <= tgChunkLimit {
		return c.Send(text, append(htmlMode, opts...)...)
	}

	var err error
	var cur strings.Builder
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		last := i == len(lines)-1
		if cur.Len()+len(line) > tgChunkLimit {
			if e := c.Send(cur.String(), telebot.ModeHTML); e != nil {
				err = e
			}
			cur.Reset()
		}
		cur.WriteString(line)
		if last && cur.Len() > 0 {
			args := append(htmlMode, opts...)
			if e := c.Send(cur.String(), args...); e != nil {
				err = e
			}
		}
	}
	return err
}
