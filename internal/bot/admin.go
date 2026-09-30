package bot

import (
	"fmt"
	"log/slog"
	"strings"

	"gopkg.in/telebot.v3"
)

// versionText — единый текст версии для /version и админ-панели,
// чтобы не разъезжались.
func versionText() string {
	return fmt.Sprintf("cs2bot %s\nсборка: %s (%s)", Version, BuildDate, BuildCommit)
}

// handleVersion показывает версию сборки только админам.
// Чужим (и пустым sender) молча ничего не отвечает, чтобы не палить
// сам факт существования команды.
func (b *Bot) handleVersion(c telebot.Context) error {
	if c.Sender() == nil || !b.isAdmin(c.Sender().ID) {
		return nil
	}
	return c.Send(versionText())
}

// handleAdmin показывает админ-панель. Неадминам молча ничего
// не отвечает — как и /version, не палит существование команды.
func (b *Bot) handleAdmin(c telebot.Context) error {
	if c.Sender() == nil || !b.isAdmin(c.Sender().ID) {
		return nil
	}
	return c.Send("🛠 <b>Админ-панель</b>", telebot.ModeHTML, b.buildAdminKeyboard())
}

func (b *Bot) buildAdminKeyboard() *telebot.ReplyMarkup {
	menu := &telebot.ReplyMarkup{}
	menu.Inline(
		menu.Row(menu.Data("📊 Статистика", "admin", "stats")),
		menu.Row(menu.Data("🔖 Версия", "admin", "version")),
	)
	return menu
}

func (b *Bot) handleAdminCallback(c telebot.Context) error {
	if c.Callback() == nil || c.Sender() == nil || !b.isAdmin(c.Sender().ID) {
		return nil
	}

	var text string
	switch strings.TrimSpace(c.Callback().Data) {
	case "stats":
		st, err := b.storage.GetStats()
		if err != nil {
			slog.Error("Ошибка получения статистики", slog.Any("error", err))
			return c.Respond(&telebot.CallbackResponse{Text: "Не удалось загрузить статистику."})
		}
		text = fmt.Sprintf("📊 <b>Статистика</b>\n\n👥 Пользователей: %d\n🔔 Подписок: %d\n⚽ Матчей в БД: %d\n\n🔖 Версия: %s (сборка %s, коммит %s)",
			st.Users, st.Subscriptions, st.Matches, Version, BuildDate, BuildCommit)
	case "version":
		text = "🔖 <b>Версия</b>\n\n" + versionText()
	default:
		return c.Respond(&telebot.CallbackResponse{Text: "Неизвестное действие."})
	}

	_ = c.Respond()
	if c.Message() == nil {
		return nil
	}
	// Клавиатуру прицепляем заново, чтобы панель оставалась живой.
	return c.Edit(text, b.buildAdminKeyboard(), telebot.ModeHTML)
}
