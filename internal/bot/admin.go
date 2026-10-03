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
	return fmt.Sprintf("cs2bot %s\nсборка: %s\nкоммит: %s", Version, BuildDate, BuildCommit)
}

// handleVersion показывает версию сборки только админам.
// Чужим (и пустым sender) молча ничего не отвечает, чтобы не палить
// сам факт существования команды.
func (b *Bot) handleVersion(c telebot.Context) error {
	if c.Sender() == nil || !b.isAdmin(c.Sender().ID) {
		return nil
	}
	return c.Send(versionText(), telebot.NoPreview)
}

// apiUsageLine форматирует использование REST API за текущий час.
// remaining — правда от сервера (заголовок X-Rate-Limit-Remaining), used —
// запросы этого процесса с запуска (рестарты и teams_puller мимо счетчика,
// поэтому used + remaining может не сходиться с limit).
func apiUsageLine(used, remaining, limit int) string {
	if limit <= 0 {
		return "📡 API: лимит не задан"
	}
	if remaining < 0 {
		return fmt.Sprintf("📡 API: остаток н/д (лимит %d), ботом с запуска: %d", limit, used)
	}
	pct := remaining * 100 / limit
	return fmt.Sprintf("📡 API: остаток %d/%d (%d%%), ботом с запуска: %d", remaining, limit, pct, used)
}

// handleAdmin показывает админ-панель. Неадминам молча ничего
// не отвечает — как и /version, не палит существование команды.
func (b *Bot) handleAdmin(c telebot.Context) error {
	if c.Sender() == nil || !b.isAdmin(c.Sender().ID) {
		return nil
	}
	return c.Send("🛠 <b>Админ-панель</b>", telebot.ModeHTML, b.buildAdminKeyboard(), telebot.NoPreview)
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
		text = fmt.Sprintf("📊 <b>Статистика</b>\n\n👥 Пользователей: %d\n🔔 Подписок: %d\n⚽ Матчей в БД: %d\n\n%s\n\n🔖 Версия: %s\nсборка: %s\nкоммит: %s",
			st.Users, st.Subscriptions, st.Matches, apiUsageLine(b.panda.APIUsage()), Version, BuildDate, BuildCommit)
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
