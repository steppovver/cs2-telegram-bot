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

		slog.Debug("Входящий запрос",
			slog.Int64("user_id", userID),
			slog.String("username", username),
			slog.String("action", action),
			slog.Duration("duration", time.Since(start)),
		)
		return err
	}
}

func (b *Bot) handleStart(c telebot.Context) error {
	return c.Send("Привет! Выбери нужное действие в меню ниже:", b.mainMenu, telebot.NoPreview)
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
	return c.Send(subscribeMenuText(), menu, telebot.NoPreview)
}

func subscribeMenuText() string {
	mins := int(subLevelGrace / time.Minute)
	return fmt.Sprintf("Выбери команды.\n\n"+
		"✅ интересная — только уведомление, что матч начался.\n"+
		"⭐ любимая — новый матч, перенос, соперник, старт и напоминание за 5 минут.\n\n"+
		"Клик по названию подписывает как на интересную. Второй клик в течение %d минут делает любимой. Клик по давней подписке снимает её.\n\n"+
		"Не нашел нужную? Просто введи часть ее названия в чат — найду и предложу подписаться.", mins)
}

func (b *Bot) handleSchedule(c telebot.Context) error {
	if c.Sender() == nil {
		return nil
	}
	userID := c.Sender().ID
	text, err := b.buildScheduleView(userID)
	if err != nil {
		slog.Error("Ошибка получения расписания", slog.Int64("user_id", userID), slog.Any("error", err))
		return c.Send("Ошибка получения расписания.", telebot.NoPreview)
	}
	return b.sendChunked(c, text)
}

// buildScheduleView собирает текст расписания: матчи команд и плей-офф
// подписанных турниров.
func (b *Bot) buildScheduleView(userID int64) (string, error) {
	subs, err := b.storage.GetUserSubscriptions(userID)
	if err != nil {
		return "", fmt.Errorf("подписки на команды: %w", err)
	}
	hasTournaments, err := b.storage.HasTournamentSubscriptions(userID)
	if err != nil {
		return "", err
	}
	if len(subs) == 0 && !hasTournaments {
		return noSubscriptionsText, nil
	}

	live, upcoming, err := b.storage.GetScheduleMatches(userID, 0, hasTournaments)
	if err != nil {
		return "", fmt.Errorf("матчи расписания: %w", err)
	}
	if len(live) == 0 && len(upcoming) == 0 {
		if hasTournaments {
			return "Для ваших команд и турниров в ближайшее время игр не найдено.", nil
		}
		return "Для ваших команд в ближайшее время игр не найдено.", nil
	}
	utcOffset, _ := b.storage.GetUserOffset(userID)
	return formatMatchesMessage("", live, upcoming, subs, utcOffset, b.maxStreams), nil
}

func (b *Bot) handleTextSearch(c telebot.Context) error {
	if b.consumeTZInput(c) {
		return nil
	}
	if b.consumeHourInput(c) {
		return nil
	}
	if c.Message() == nil || c.Sender() == nil {
		return nil
	}
	query := strings.TrimSpace(c.Message().Text)
	if len([]rune(query)) < 2 {
		return c.Send("Введите хотя бы 2 символа для поиска.", telebot.NoPreview)
	}

	teams, err := b.storage.SearchTeams(query)
	if err != nil || len(teams) == 0 {
		return c.Send("Команды не найдены.", b.mainMenu, telebot.NoPreview)
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
	if err := b.sendChunked(c, sb.String(), menu); err != nil {
		return err
	}
	// Сообщение выше несет инлайн-кнопки, а Telegram разрешает только один
	// reply_markup на сообщение — главную клавиатуру возвращаем отдельно.
	return b.restoreMainMenu(c)
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

	var current *domain.TeamInfo
	for i := range subs {
		if subs[i].ID == int(teamID) {
			current = &subs[i]
			break
		}
	}
	subscribed := current != nil
	favorite := subscribed && current.Favorite
	var subscribedAt time.Time
	teamName := ""
	if subscribed {
		teamName = current.Name
		subscribedAt = current.SubscribedAt
	}
	if teams, err := b.storage.GetTeamsByIDs([]int{int(teamID)}); err == nil && len(teams) > 0 && teams[0].Name != "" {
		teamName = teams[0].Name
	}
	if teamName == "" {
		teamName = fmt.Sprintf("команда %d", teamID)
	}

	action := nextSubClick(subscribed, favorite, subscribedAt, time.Now())
	var toastMsg string
	switch action {
	case subClickInterest:
		err = b.storage.Subscribe(userID, teamID, teamName, false)
		toastMsg = fmt.Sprintf("%s — интересная: только старт", teamName)
	case subClickFavorite:
		err = b.storage.Subscribe(userID, teamID, teamName, true)
		toastMsg = fmt.Sprintf("%s — любимая: все уведомления", teamName)
	default:
		err = b.storage.Unsubscribe(userID, teamID)
		toastMsg = fmt.Sprintf("%s — подписка снята", teamName)
	}

	if err != nil {
		slog.Error("Ошибка изменения подписки в БД", slog.Int64("user_id", userID), slog.Int64("team_id", teamID), slog.Any("error", err))
		return c.Respond(&telebot.CallbackResponse{Text: "Не удалось сохранить изменения."})
	}

	slog.Info("Изменение подписки на команду",
		slog.Int64("user_id", userID),
		slog.Int64("team_id", teamID),
		slog.String("team", teamName),
		slog.Int("action", int(action)))

	_ = c.Respond(&telebot.CallbackResponse{Text: toastMsg})

	if c.Message() == nil {
		return nil
	}
	markup := c.Message().ReplyMarkup
	if markup == nil {
		return nil
	}
	newSub, newFav := false, false
	switch action {
	case subClickInterest:
		newSub = true
	case subClickFavorite:
		newSub, newFav = true, true
	}
	label := teamButtonLabel(teamName, newSub, newFav)
	for i, row := range markup.InlineKeyboard {
		for j, btn := range row {
			btnTeamID, ok := parseTeamIDFromButton(btn.Data)
			if !ok || btnTeamID != teamID {
				continue
			}
			markup.InlineKeyboard[i][j].Text = label
		}
	}
	return c.Edit(c.Message().Text, markup, telebot.ModeHTML)
}

func (b *Bot) buildTeamsKeyboard(actionPrefix string, teamsToDisplay []domain.TeamInfo, userSubs []domain.TeamInfo) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}
	var rows []telebot.Row
	var currentRow []telebot.Btn

	for _, t := range teamsToDisplay {
		payload := strconv.Itoa(t.ID)
		subscribed, favorite := false, false
		for _, sub := range userSubs {
			if sub.ID == t.ID {
				subscribed = true
				favorite = sub.Favorite
				break
			}
		}
		currentRow = append(currentRow, menu.Data(teamButtonLabel(t.Name, subscribed, favorite), actionPrefix, payload))

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

// sendChunked отправляет длинный HTML-текст кусками по строкам.
// Дополнительные opts (например, inline-меню) цепляются к последнему куску.
func (b *Bot) sendChunked(c telebot.Context, text string, opts ...interface{}) error {
	htmlMode := []interface{}{telebot.ModeHTML, telebot.NoPreview}
	chunks := chunkLines(text, tgChunkLimit)
	if len(chunks) == 1 {
		return c.Send(chunks[0], append(htmlMode, opts...)...)
	}

	var err error
	for i, chunk := range chunks {
		if i == len(chunks)-1 {
			args := append(htmlMode, opts...)
			if e := c.Send(chunk, args...); e != nil {
				err = e
			}
			continue
		}
		if e := c.Send(chunk, telebot.ModeHTML, telebot.NoPreview); e != nil {
			err = e
		}
	}
	return err
}

// restoreMainMenu возвращает главную клавиатуру после текстового ввода.
// Набор текста сворачивает ее на клиенте, а сообщение с инлайн-кнопками
// вторую клавиатуру прицепить не может (лимит Telegram: один reply_markup
// на сообщение) — поэтому меню возвращаем отдельным сообщением.
func (b *Bot) restoreMainMenu(c telebot.Context) error {
	return c.Send("Выбери нужное действие в меню ниже:", b.mainMenu, telebot.NoPreview)
}
