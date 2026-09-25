package bot

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"cs2bot/internal/domain"

	"gopkg.in/telebot.v3"
)

var defaultDigestPresetHours = []int{9, 10, 11, 12}

// digestPresetHours возвращает пресеты из конфига с защитой от пустого значения.
func (b *Bot) digestPresetHours() []int {
	if len(b.digestHours) == 0 {
		return defaultDigestPresetHours
	}
	return b.digestHours
}

func (b *Bot) handleDigestMenu(c telebot.Context) error {
	if c.Sender() == nil {
		return nil
	}
	settings, err := b.storage.GetDigestSettings(c.Sender().ID)
	if err != nil {
		slog.Error("Ошибка получения настроек дайджеста", slog.Int64("user_id", c.Sender().ID), slog.Any("error", err))
		return c.Send("Не удалось загрузить настройки. Попробуйте позже.")
	}
	return c.Send(digestStatusText(settings.Enabled, settings.Hour), telebot.ModeHTML, b.buildDigestKeyboardRaw(settings.Enabled, settings.Hour))
}

func (b *Bot) handleDigestCallback(c telebot.Context) error {
	if c.Callback() == nil || c.Sender() == nil {
		return nil
	}
	userID := c.Sender().ID
	data := strings.TrimSpace(c.Callback().Data)

	switch {
	case data == "toggle":
		b.clearHourInput(userID)
		settings, err := b.storage.GetDigestSettings(userID)
		if err != nil {
			slog.Error("Ошибка получения настроек дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Внутренняя ошибка. Попробуйте позже."})
		}
		if err := b.storage.SetDigestEnabled(userID, !settings.Enabled); err != nil {
			slog.Error("Ошибка изменения настроек дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Не удалось сохранить."})
		}
		settings.Enabled = !settings.Enabled
		toast := "Ежедневный дайджест включен!"
		if !settings.Enabled {
			toast = "Ежедневный дайджест выключен."
		}
		_ = c.Respond(&telebot.CallbackResponse{Text: toast})
		return b.editDigestMessage(c, settings.Enabled, settings.Hour)

	case data == "hour_custom":
		b.awaitingHourMu.Lock()
		b.awaitingHour[userID] = true
		b.awaitingHourMu.Unlock()
		_ = c.Respond(&telebot.CallbackResponse{Text: "Введите час"})
		return c.Send("Введите час от 0 до 23 (по Москве), в который присылать дайджест:", b.mainMenu)

	case strings.HasPrefix(data, "hour_"):
		b.clearHourInput(userID)
		hour, err := strconv.Atoi(strings.TrimPrefix(data, "hour_"))
		if err != nil || hour < 0 || hour > 23 {
			return c.Respond(&telebot.CallbackResponse{Text: "Некорректный час."})
		}
		if err := b.storage.SetDigestHour(userID, hour); err != nil {
			slog.Error("Ошибка изменения часа дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Не удалось сохранить."})
		}
		settings, err := b.storage.GetDigestSettings(userID)
		if err != nil {
			return c.Respond(&telebot.CallbackResponse{Text: "Час сохранен."})
		}
		_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("Время дайджеста: %d:00 МСК", hour)})
		return b.editDigestMessage(c, settings.Enabled, settings.Hour)

	default:
		return c.Respond(&telebot.CallbackResponse{Text: "Неизвестное действие."})
	}
}

func (b *Bot) editDigestMessage(c telebot.Context, enabled bool, hour int) error {
	if c.Message() == nil {
		return nil
	}
	return c.Edit(digestStatusText(enabled, hour), b.buildDigestKeyboardRaw(enabled, hour), telebot.ModeHTML)
}

func digestStatusText(enabled bool, hour int) string {
	status := "выключен ❌"
	if enabled {
		status = "включен ✅"
	}
	return fmt.Sprintf("⏰ <b>Ежедневный дайджест</b>\n\nСтатус: %s\nВремя: %d:00 МСК\n\nКаждое утро пришлем матчи ваших команд на 24 часа вперед. Если матчей нет — промолчим.",
		status, hour)
}

func (b *Bot) buildDigestKeyboardRaw(enabled bool, hour int) *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}

	toggleText := "✅ Включить"
	if enabled {
		toggleText = "❌ Выключить"
	}
	rows := []telebot.Row{
		menu.Row(menu.Data(toggleText, "digest", "toggle")),
	}

	var hourRow []telebot.Btn
	for _, h := range b.digestPresetHours() {
		text := fmt.Sprintf("%d:00", h)
		if h == hour {
			text = "✅ " + text
		}
		hourRow = append(hourRow, menu.Data(text, "digest", fmt.Sprintf("hour_%d", h)))
	}
	rows = append(rows, menu.Row(hourRow...))
	rows = append(rows, menu.Row(menu.Data("⌨ Свой час", "digest", "hour_custom")))

	menu.Inline(rows...)
	return menu
}

// clearHourInput снимает ожидание ввода часа (пользователь передумал / выбрал пресет).
func (b *Bot) clearHourInput(userID int64) {
	b.awaitingHourMu.Lock()
	delete(b.awaitingHour, userID)
	b.awaitingHourMu.Unlock()
}

// consumeHourInput перехватывает текстовый ввод часа после кнопки "Свой час".
// Возвращает true, если сообщение обработано как ввод часа.
func (b *Bot) consumeHourInput(c telebot.Context) bool {
	if c.Message() == nil || c.Sender() == nil {
		return false
	}
	userID := c.Sender().ID

	b.awaitingHourMu.Lock()
	awaiting := b.awaitingHour[userID]
	if !awaiting {
		b.awaitingHourMu.Unlock()
		return false
	}
	b.awaitingHourMu.Unlock()

	raw := strings.TrimSpace(c.Message().Text)
	hour, err := strconv.Atoi(raw)
	if err != nil || hour < 0 || hour > 23 {
		_ = c.Send("Нужно число от 0 до 23. Попробуйте еще раз:", b.mainMenu)
		return true
	}

	if err := b.storage.SetDigestHour(userID, hour); err != nil {
		slog.Error("Ошибка изменения часа дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
		_ = c.Send("Не удалось сохранить. Попробуйте позже.", b.mainMenu)
		b.awaitingHourMu.Lock()
		delete(b.awaitingHour, userID)
		b.awaitingHourMu.Unlock()
		return true
	}

	b.awaitingHourMu.Lock()
	delete(b.awaitingHour, userID)
	b.awaitingHourMu.Unlock()

	settings, err := b.storage.GetDigestSettings(userID)
	if err != nil {
		return true
	}
	_ = c.Send(fmt.Sprintf("Время дайджеста: %d:00 МСК", hour), b.mainMenu)
	_ = c.Send(digestStatusText(settings.Enabled, settings.Hour), telebot.ModeHTML, b.buildDigestKeyboardRaw(settings.Enabled, settings.Hour))
	return true
}

func moscowLocation() *time.Location {
	if loc, err := time.LoadLocation("Europe/Moscow"); err == nil {
		return loc
	}
	slog.Warn("tzdata Europe/Moscow недоступна, используем фиксированный часовой пояс +3")
	return time.FixedZone("MSK", 3*3600)
}

func (b *Bot) StartDailyDigest(ctx context.Context) {
	loc := moscowLocation()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Воркер дайджеста завершил работу")
			return
		case <-ticker.C:
			b.runDigestCycle(ctx, loc)
		}
	}
}

func (b *Bot) runDigestCycle(ctx context.Context, loc *time.Location) {
	now := time.Now()
	msk := now.In(loc)
	today := msk.Format("2006-01-02")
	hour := msk.Hour()
	slog.Debug("Старт цикла дайджеста",
		slog.String("msk", msk.Format("2006-01-02 15:04")),
		slog.Int("hour", hour),
		slog.String("today", today))

	users, err := b.storage.GetDigestDueUsers(hour, today)
	if err != nil {
		slog.Error("Ошибка получения получателей дайджеста", slog.Any("error", err))
		return
	}
	if len(users) == 0 {
		slog.Debug("Нет получателей дайджеста на этот час")
		return
	}
	slog.Debug("Получатели дайджеста", slog.Int("count", len(users)))

	until := now.Add(24 * time.Hour)
	for _, userID := range users {
		select {
		case <-ctx.Done():
			return
		default:
		}
		b.processDigestUser(ctx, userID, now, until, today)
	}
}

// processDigestUser готовит и ставит в очередь персональный дайджест одного юзера.
// Пустой результат тоже фиксирует как отправленный, чтобы не дергать БД весь час.
func (b *Bot) processDigestUser(ctx context.Context, userID int64, now, until time.Time, today string) {
	matches, err := b.storage.GetDigestMatches(userID, now.Unix(), until.Unix())
	if err != nil {
		slog.Error("Ошибка получения матчей для дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
		return
	}
	slog.Debug("Матчи для дайджеста",
		slog.Int64("user_id", userID),
		slog.Int("found", len(matches)))

	subs, _ := b.storage.GetUserSubscriptions(userID)
	lines := buildDigestLines(matches, subs)

	if len(lines) == 0 {
		slog.Debug("Дайджест пуст, матчей на 24 часа нет",
			slog.Int64("user_id", userID))
		b.markDigestSent(userID, today)
		return
	}

	chunks := splitDigestText("⏰ <b>Матчи ваших команд на 24 часа:</b>\n\n" + strings.Join(lines, "\n") + "\n")
	if !b.enqueueDigest(ctx, userID, chunks) {
		slog.Warn("Дайджест не поставлен в очередь (переполнение), повтор на следующем тике",
			slog.Int64("user_id", userID))
		return
	}
	b.markDigestSent(userID, today)
	slog.Debug("Дайджест поставлен в очередь",
		slog.Int64("user_id", userID),
		slog.Int("lines", len(lines)),
		slog.Int("chunks", len(chunks)))
}

// buildDigestLines фильтрует матчи без соперника/времени и форматирует
// их одной строкой на матч, подсвечивая команды пользователя.
func buildDigestLines(matches []domain.Match, subs []domain.TeamInfo) []string {
	var lines []string
	for _, m := range matches {
		if m.TeamA == "TBD" || m.TeamB == "TBD" || m.Time.IsZero() {
			continue
		}
		timeStr := formatTGTime(m.Time, "dt", "02.01 15:04 UTC")
		teamA, teamB := html.EscapeString(m.TeamA), html.EscapeString(m.TeamB)
		for _, sub := range subs {
			if sub.ID == m.TeamAID {
				teamA = "<b>" + teamA + "</b>"
			}
			if sub.ID == m.TeamBID {
				teamB = "<b>" + teamB + "</b>"
			}
		}
		lines = append(lines, fmt.Sprintf("%s | %s vs %s", timeStr, teamA, teamB))
	}
	return lines
}

// enqueueDigest неблокирующе кладет чанки в очередь рассылки.
// false — очередь полна или контекст отменен, вызывающий код повторит позже.
func (b *Bot) enqueueDigest(ctx context.Context, userID int64, chunks []string) bool {
	for _, chunk := range chunks {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		select {
		case b.broadcastCh <- BroadcastTask{UserID: userID, Text: chunk}:
		default:
			return false
		}
	}
	return true
}

func (b *Bot) markDigestSent(userID int64, today string) {
	if err := b.storage.MarkDigestSent(userID, today); err != nil {
		slog.Error("Ошибка отметки дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
	}
}

func splitDigestText(text string) []string {
	const limit = 3500
	if len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if cur.Len()+len(line) > limit {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}
