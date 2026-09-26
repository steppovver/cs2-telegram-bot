package bot

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

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
	wall := wallHour(settings.HourUTC, settings.UtcOffset)
	return c.Send(digestStatusText(settings.Enabled, wall, settings.UtcOffset), telebot.ModeHTML, b.buildDigestKeyboardRaw(settings.Enabled, wall, settings.UtcOffset))
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
		b.clearTZInput(userID)
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
		return b.editDigestMessage(c, settings.Enabled, wallHour(settings.HourUTC, settings.UtcOffset), settings.UtcOffset)

	case data == "tz_minus" || data == "tz_plus":
		b.clearHourInput(userID)
		b.clearTZInput(userID)
		settings, err := b.storage.GetDigestSettings(userID)
		if err != nil {
			return c.Respond(&telebot.CallbackResponse{Text: "Внутренняя ошибка. Попробуйте позже."})
		}
		wall := wallHour(settings.HourUTC, settings.UtcOffset)
		off := settings.UtcOffset
		if data == "tz_minus" {
			off--
			if off < minUTCOffset {
				off = maxUTCOffset
			}
		} else {
			off++
			if off > maxUTCOffset {
				off = minUTCOffset
			}
		}
		// Сохраняем wall-time: сдвигаем хранимый UTC-час заодно с поясом.
		newHourUTC := wantUTCHour(wall, off)
		if err := b.storage.SetUserOffset(userID, off); err != nil {
			slog.Error("Ошибка изменения пояса", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Не удалось сохранить."})
		}
		if err := b.storage.SetDigestHour(userID, newHourUTC); err != nil {
			slog.Error("Ошибка сдвига часа дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Не удалось сохранить."})
		}
		_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("Часовой пояс: UTC%+d", off)})
		return b.editDigestMessage(c, settings.Enabled, wall, off)

	case data == "tz_custom":
		b.clearHourInput(userID)
		b.awaitingTZMu.Lock()
		b.awaitingTZ[userID] = true
		b.awaitingTZMu.Unlock()
		_ = c.Respond(&telebot.CallbackResponse{Text: "Введите сдвиг"})
		return c.Send("Введите сдвиг от UTC в часах от -12 до +14 (например: 3, -5, +10):", b.mainMenu)

	case data == "tz_noop":
		return c.Respond()

	case data == "hour_custom":
		b.awaitingHourMu.Lock()
		b.awaitingHour[userID] = true
		b.awaitingHourMu.Unlock()
		b.clearTZInput(userID)
		_ = c.Respond(&telebot.CallbackResponse{Text: "Введите час"})
		off, _ := b.storage.GetUserOffset(userID)
		return c.Send(fmt.Sprintf("Введите час от 0 до 23 в вашем поясе (UTC%+d), в который присылать дайджест:", off), b.mainMenu)

	case strings.HasPrefix(data, "hour_"):
		b.clearHourInput(userID)
		b.clearTZInput(userID)
		wall, err := strconv.Atoi(strings.TrimPrefix(data, "hour_"))
		if err != nil || wall < 0 || wall > 23 {
			return c.Respond(&telebot.CallbackResponse{Text: "Некорректный час."})
		}
		settings, err := b.storage.GetDigestSettings(userID)
		if err != nil {
			return c.Respond(&telebot.CallbackResponse{Text: "Внутренняя ошибка. Попробуйте позже."})
		}
		if err := b.storage.SetDigestHour(userID, wantUTCHour(wall, settings.UtcOffset)); err != nil {
			slog.Error("Ошибка изменения часа дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Не удалось сохранить."})
		}
		_ = c.Respond(&telebot.CallbackResponse{Text: fmt.Sprintf("Время дайджеста: %d:00 UTC%+d", wall, settings.UtcOffset)})
		return b.editDigestMessage(c, settings.Enabled, wall, settings.UtcOffset)

	default:
		return c.Respond(&telebot.CallbackResponse{Text: "Неизвестное действие."})
	}
}

func (b *Bot) editDigestMessage(c telebot.Context, enabled bool, hour, utcOffset int) error {
	if c.Message() == nil {
		return nil
	}
	return c.Edit(digestStatusText(enabled, hour, utcOffset), b.buildDigestKeyboardRaw(enabled, hour, utcOffset), telebot.ModeHTML)
}

func digestStatusText(enabled bool, hour, utcOffset int) string {
	status := "выключен ❌"
	if enabled {
		status = "включен ✅"
	}
	return fmt.Sprintf("⏰ <b>Ежедневный дайджест</b>\n\nСтатус: %s\nВремя: %d:00 UTC%+d\n\nКаждое утро пришлем матчи ваших команд на 24 часа вперед. Если матчей нет — промолчим.",
		status, hour, utcOffset)
}

func (b *Bot) buildDigestKeyboardRaw(enabled bool, hour, utcOffset int) *telebot.ReplyMarkup {
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

	tzLabel := fmt.Sprintf("🌍 UTC%+d", utcOffset)
	rows = append(rows, menu.Row(
		menu.Data("➖", "digest", "tz_minus"),
		menu.Data(tzLabel, "digest", "tz_noop"),
		menu.Data("➕", "digest", "tz_plus"),
	))
	rows = append(rows, menu.Row(menu.Data("⌨ Свой пояс", "digest", "tz_custom")))

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
	wall, err := strconv.Atoi(raw)
	if err != nil || wall < 0 || wall > 23 {
		_ = c.Send("Нужно число от 0 до 23. Попробуйте еще раз:", b.mainMenu)
		return true
	}

	settings, err := b.storage.GetDigestSettings(userID)
	if err != nil {
		_ = c.Send("Не удалось сохранить. Попробуйте позже.", b.mainMenu)
		b.awaitingHourMu.Lock()
		delete(b.awaitingHour, userID)
		b.awaitingHourMu.Unlock()
		return true
	}

	if err := b.storage.SetDigestHour(userID, wantUTCHour(wall, settings.UtcOffset)); err != nil {
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

	settings, err = b.storage.GetDigestSettings(userID)
	if err != nil {
		return true
	}
	_ = c.Send(fmt.Sprintf("Время дайджеста: %d:00 UTC%+d", wall, settings.UtcOffset), b.mainMenu)
	_ = c.Send(digestStatusText(settings.Enabled, wall, settings.UtcOffset), telebot.ModeHTML, b.buildDigestKeyboardRaw(settings.Enabled, wall, settings.UtcOffset))
	return true
}

// clearTZInput снимает ожидание ввода пояса.
func (b *Bot) clearTZInput(userID int64) {
	b.awaitingTZMu.Lock()
	delete(b.awaitingTZ, userID)
	b.awaitingTZMu.Unlock()
}

// consumeTZInput перехватывает текстовый ввод пояса после кнопки "Свой пояс".
func (b *Bot) consumeTZInput(c telebot.Context) bool {
	if c.Message() == nil || c.Sender() == nil {
		return false
	}
	userID := c.Sender().ID

	b.awaitingTZMu.Lock()
	awaiting := b.awaitingTZ[userID]
	if !awaiting {
		b.awaitingTZMu.Unlock()
		return false
	}
	b.awaitingTZMu.Unlock()

	raw := strings.TrimSpace(c.Message().Text)
	off, ok := parseUTCOffset(raw)
	if !ok {
		_ = c.Send("Нужно число от -12 до +14. Например: 3, -5, +10. Попробуйте еще раз:", b.mainMenu)
		return true
	}

	settings, err := b.storage.GetDigestSettings(userID)
	if err != nil {
		_ = c.Send("Не удалось сохранить. Попробуйте позже.", b.mainMenu)
		b.awaitingTZMu.Lock()
		delete(b.awaitingTZ, userID)
		b.awaitingTZMu.Unlock()
		return true
	}
	// Сохраняем wall-time: сдвигаем хранимый UTC-час заодно с поясом.
	wall := wallHour(settings.HourUTC, settings.UtcOffset)
	newHourUTC := wantUTCHour(wall, off)

	if err := b.storage.SetUserOffset(userID, off); err != nil {
		slog.Error("Ошибка изменения пояса", slog.Int64("user_id", userID), slog.Any("error", err))
		_ = c.Send("Не удалось сохранить. Попробуйте позже.", b.mainMenu)
		b.awaitingTZMu.Lock()
		delete(b.awaitingTZ, userID)
		b.awaitingTZMu.Unlock()
		return true
	}
	if err := b.storage.SetDigestHour(userID, newHourUTC); err != nil {
		slog.Error("Ошибка сдвига часа дайджеста", slog.Int64("user_id", userID), slog.Any("error", err))
		_ = c.Send("Не удалось сохранить. Попробуйте позже.", b.mainMenu)
		b.awaitingTZMu.Lock()
		delete(b.awaitingTZ, userID)
		b.awaitingTZMu.Unlock()
		return true
	}

	b.awaitingTZMu.Lock()
	delete(b.awaitingTZ, userID)
	b.awaitingTZMu.Unlock()

	_ = c.Send(fmt.Sprintf("Часовой пояс: UTC%+d", off), b.mainMenu)
	_ = c.Send(digestStatusText(settings.Enabled, wall, off), telebot.ModeHTML, b.buildDigestKeyboardRaw(settings.Enabled, wall, off))
	return true
}
