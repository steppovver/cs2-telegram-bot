package bot

import (
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"cs2bot/internal/domain"

	"gopkg.in/telebot.v3"
)

// handleScore показывает счет по явному запросу (кнопка 📊): идущие сейчас
// и завершенные матчи команд пользователя. Только из БД, без API.
// Счет нигде не пушится сам — защита от спойлеров.
func (b *Bot) handleScore(c telebot.Context) error {
	if c.Sender() == nil {
		return nil
	}
	userID := c.Sender().ID
	subs, err := b.storage.GetUserSubscriptions(userID)
	if err != nil {
		return c.Send("Произошла ошибка при обращении к базе данных.", telebot.NoPreview)
	}

	if len(subs) == 0 {
		return c.Send("Вы еще не подписаны ни на одну команду.\nНажмите «🔔 Подписки на команды».", telebot.NoPreview)
	}

	matches, err := b.storage.GetScoreMatches(userID)
	if err != nil {
		slog.Error("Ошибка получения матчей со счетом", slog.Int64("user_id", userID), slog.Any("error", err))
		return c.Send("Ошибка получения счета.", telebot.NoPreview)
	}
	utcOffset, _ := b.storage.GetUserOffset(userID)

	var running, finished []domain.Match
	for _, m := range matches {
		switch m.Status {
		case "running":
			running = append(running, m)
		case "finished", "post_match":
			finished = append(finished, m)
		}
	}
	// Завершенные — свежие сверху (селект отдает по возрастанию).
	for i, j := 0, len(finished)-1; i < j; i, j = i+1, j-1 {
		finished[i], finished[j] = finished[j], finished[i]
	}

	if len(running) == 0 && len(finished) == 0 {
		return c.Send("Пока нет матчей со счетом для ваших команд.", telebot.NoPreview)
	}

	var sb strings.Builder
	writeGroup := func(title string, group []domain.Match, showDate bool, needDivider *bool) {
		if len(group) == 0 {
			return
		}
		if *needDivider {
			sb.WriteString("➖➖➖➖➖➖➖\n")
		}
		sb.WriteString(title + "\n\n")
		for i, m := range group {
			if i > 0 {
				sb.WriteString("➖➖➖➖➖➖➖\n")
			}
			sb.WriteString(buildScoreBlock(m, subs, utcOffset, showDate))
		}
		sb.WriteString("\n")
		*needDivider = true
	}

	needDivider := false
	writeGroup("🔴 <b>Сейчас идут:</b>", running, false, &needDivider)
	writeGroup("✅ <b>Завершенные:</b>", finished, true, &needDivider)

	return b.sendChunked(c, strings.TrimSuffix(sb.String(), "\n"))
}

// buildScoreBlock форматирует один матч со счетом.
// showDate=true добавляет строку даты проведения (для завершенных).
func buildScoreBlock(m domain.Match, subs []domain.TeamInfo, utcOffset int, showDate bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🎮 %s vs %s\n", highlightTeam(m.TeamA, m.TeamAID, subs), highlightTeam(m.TeamB, m.TeamBID, subs)))
	if showDate && !m.Time.IsZero() {
		sb.WriteString(fmt.Sprintf("📅 Сыгран: %s\n", formatTGTime(m.Time, "d", "02.01.2006", utcOffset)))
	}
	if line := seriesLine(m); line != "" {
		sb.WriteString(line + "\n")
	}
	for _, line := range gameLines(m) {
		sb.WriteString(line + "\n")
	}
	if line := durationLine(m); line != "" {
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// highlightTeam подсвечивает команды пользователя, как в расписании.
func highlightTeam(name string, id int, subs []domain.TeamInfo) string {
	esc := html.EscapeString(name)
	for _, sub := range subs {
		if sub.ID == id {
			return "<b>" + esc + "</b>"
		}
	}
	return esc
}

// boSuffix возвращает " (BO3)" для заголовка матча. 0 = неизвестно, без суффикса.
func boSuffix(m domain.Match) string {
	if m.NumberOfGames <= 0 {
		return ""
	}
	return fmt.Sprintf(" (BO%d)", m.NumberOfGames)
}

// teamNameByID возвращает имя команды по ID или "" если неизвестно.
func teamNameByID(m domain.Match, id int) string {
	switch id {
	case m.TeamAID:
		return m.TeamA
	case m.TeamBID:
		return m.TeamB
	default:
		return ""
	}
}

// seriesLine строит строку счета серии в порядке команд заголовка.
// Нет данных — пустая строка (блок пропускается).
func seriesLine(m domain.Match) string {
	if len(m.Results) == 0 {
		return ""
	}
	scores := make(map[int]int, len(m.Results))
	for _, r := range m.Results {
		scores[r.TeamID] = r.Score
	}
	a, okA := scores[m.TeamAID]
	bb, okB := scores[m.TeamBID]
	if !okA || !okB {
		return ""
	}
	format := "🏆 Серия"
	if m.NumberOfGames > 0 {
		format = fmt.Sprintf("🏆 Серия (BO%d)", m.NumberOfGames)
	}
	return fmt.Sprintf("%s: %s %d — %d %s",
		format, html.EscapeString(m.TeamA), a, bb, html.EscapeString(m.TeamB))
}

// gameLines строит строки по картам: победитель для законченных,
// "идет сейчас" для текущей.
func gameLines(m domain.Match) []string {
	var lines []string
	for _, g := range m.Games {
		winner := teamNameByID(m, g.WinnerID)
		switch {
		case winner != "":
			lines = append(lines, fmt.Sprintf("🗺 Карта %d — %s", g.Position, html.EscapeString(winner)))
		case g.Status == "running":
			lines = append(lines, fmt.Sprintf("🗺 Карта %d — идет сейчас", g.Position))
		}
	}
	return lines
}

// durationLine строит строку длительности: для идущих — от начала до сейчас,
// для завершенных — от начала до конца. Нет данных — пустая строка.
func durationLine(m domain.Match) string {
	if m.Time.IsZero() {
		return ""
	}
	if m.Status == "running" {
		if d := time.Since(m.Time); d > 0 {
			return "⏱ Идет: " + formatDuration(d)
		}
		return ""
	}
	if !m.EndAt.IsZero() && m.EndAt.After(m.Time) {
		return "⏱ Длился: " + formatDuration(m.EndAt.Sub(m.Time))
	}
	return ""
}

// formatDuration показывает длительность как "47 мин" или "1 ч 23 мин".
func formatDuration(d time.Duration) string {
	total := int(d.Minutes())
	if total < 1 {
		return "меньше минуты"
	}
	if total < 60 {
		return fmt.Sprintf("%d мин", total)
	}
	return fmt.Sprintf("%d ч %02d мин", total/60, total%60)
}
